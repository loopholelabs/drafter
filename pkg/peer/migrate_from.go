package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"golang.org/x/sys/unix"
)

type MigrateFromDevice[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Name      string `json:"name"`
	Base      string `json:"base"`
	Overlay   string `json:"overlay"`
	State     string `json:"state"`
	BlockSize uint32 `json:"blockSize"`
	Shared    bool   `json:"shared"`
}

func (peer *Peer[L, R, G]) MigrateFrom(
	ctx context.Context,
	devices []MigrateFromDevice[L, R, G],
	readers []io.Reader,
	writers []io.Writer,
	hooks mounter.MigrateFromHooks,
) (
	migratedPeer *MigratedPeer[L, R, G],
	errs error,
) {

	migratedPeer = &MigratedPeer[L, R, G]{
		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
		devices:      devices,
		runner:       peer.runner,
		stage2Inputs: []migrateFromStage{},
	}

	var (
		allRemoteDevicesReceived       = make(chan struct{})
		signalAllRemoteDevicesReceived = sync.OnceFunc(func() {
			close(allRemoteDevicesReceived) // We can safely close() this channel since the caller only runs once/is `sync.OnceFunc`d
		})

		allRemoteDevicesReady       = make(chan struct{})
		signalAllRemoteDevicesReady = sync.OnceFunc(func() {
			close(allRemoteDevicesReady) // We can safely close() this channel since the caller only runs once/is `sync.OnceFunc`d
		})
	)

	// We don't `defer cancelProtocolCtx()` this because we cancel in the wait function
	protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

	// We overwrite this further down, but this is so that we don't leak the `protocolCtx` if we `panic()` before we set `WaitForMigrationsToComplete`
	migratedPeer.Wait = func() error {
		cancelProtocolCtx()

		return nil
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	// Use an atomic counter and `allDevicesReady` and instead of a WaitGroup so that we can `select {}` without leaking a goroutine
	var (
		receivedButNotReadyRemoteDevices atomic.Int32

		deviceCloseFuncsLock sync.Mutex
		deviceCloseFuncs     []func() error

		stage2InputsLock sync.Mutex

		pro *protocol.RW
	)

	addDeviceCloseFunc := func(f func() error) {
		deviceCloseFuncsLock.Lock()
		deviceCloseFuncs = append(deviceCloseFuncs, f)
		deviceCloseFuncs = append(deviceCloseFuncs, peer.runner.Close) // defer runner.Close()
		deviceCloseFuncsLock.Unlock()
	}

	stageOutputCb := func(mfs migrateFromStage) {
		stage2InputsLock.Lock()
		migratedPeer.stage2Inputs = append(migratedPeer.stage2Inputs, mfs)
		stage2InputsLock.Unlock()
	}

	// When a device is received and exposed, we call this...
	exposedCb := func(index int, name string, devicePath string) error {
		fmt.Printf("Expose dev %s %s\n", name, devicePath)
		deviceInfo, err := os.Stat(devicePath)
		if err != nil {
			return errors.Join(snapshotter.ErrCouldNotGetDeviceStat, err)
		}

		deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return ErrCouldNotGetNBDDeviceStat
		}

		deviceMajor := uint64(deviceStat.Rdev / 256)
		deviceMinor := uint64(deviceStat.Rdev % 256)
		deviceID := int((deviceMajor << 8) | deviceMinor)

		select {
		case <-goroutineManager.Context().Done():
			if err := goroutineManager.Context().Err(); err != nil {
				return errors.Join(ErrPeerContextCancelled, err)
			}
			return nil
		default:
			if err := unix.Mknod(filepath.Join(peer.runner.VMPath, name), unix.S_IFBLK|0666, deviceID); err != nil {
				return errors.Join(ErrCouldNotCreateDeviceNode, err)
			}
		}
		if hook := hooks.OnLocalDeviceExposed; hook != nil {
			hook(uint32(index), devicePath)
		}
		return nil
	}

	// Silo event handler
	eventHandler := func(e *packets.Event) {
		index := uint32(0)
		switch e.Type {
		case packets.EventCustom:
			switch e.CustomType {
			case byte(registry.EventCustomAllDevicesSent):
				signalAllRemoteDevicesReceived()

				if hook := hooks.OnRemoteAllDevicesReceived; hook != nil {
					hook()
				}

			case byte(registry.EventCustomTransferAuthority):
				if receivedButNotReadyRemoteDevices.Add(-1) <= 0 {
					signalAllRemoteDevicesReady()
				}

				if hook := hooks.OnRemoteDeviceAuthorityReceived; hook != nil {
					hook(index)
				}
			}

		case packets.EventCompleted:
			if hook := hooks.OnRemoteDeviceMigrationCompleted; hook != nil {
				hook(index)
			}
		}
	}

	if len(readers) > 0 && len(writers) > 0 { // Only open the protocol if we want passed in readers and writers
		pro = protocol.NewRW(
			protocolCtx, // We don't track this because we return the wait function
			readers,
			writers,
			nil)

		// Do this in a goroutine for now...
		go func() {
			fmt.Printf("MigrateFrom...\n")
			// For now...
			names := make([]string, 0)
			var namesLock sync.Mutex
			tweak := func(index int, name string, schema string) string {
				// Add it to stage2Inputs so we don't request it locally...
				stageOutputCb(migrateFromStage{
					name:   name,
					id:     uint32(index),
					remote: true,
				})

				namesLock.Lock()
				names = append(names, name)
				namesLock.Unlock()

				/*
					// Modify the schema here...
					devschema := config.SiloSchema{}
					err := devschema.Decode([]byte(schema))
					if err != nil || len(devschema.Device) != 1 {
						panic(err)
					}
					d := devschema.Device[0]
					d.ROSource = nil
					d.System = "file"
					filename := filepath.Base(d.Location)
					d.Location = path.Join("image", "instance-1", filename)
				*/
				s := strings.ReplaceAll(schema, "instance-0", "instance-1")
				//s := d.EncodeAsBlock()
				fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
				return string(s)
			}
			events := func(e *packets.Event) {
				fmt.Printf("Event received %v, pass it on to the handler...\n", e)
				eventHandler(e)
			}
			dg, err := devicegroup.NewFromProtocol(context.TODO(), pro, tweak, events, nil, nil)
			if err != nil {
				fmt.Printf("Error migrating %v\n", err)
			}

			for _, n := range names {
				dev := dg.GetExposedDeviceByName(n)
				if dev != nil {
					fmt.Printf("GOT Device %s %v\n", n, dev.Device())
					err := exposedCb(0, n, filepath.Join("/dev", dev.Device()))
					fmt.Printf("ExposedCb err %v\n", err)
				}
			}

			dg.WaitForCompletion()

			// FIXME: Save dg for future migrations.
			addDeviceCloseFunc(dg.CloseAll)
		}()
	}

	migratedPeer.Wait = sync.OnceValue(func() error {
		defer cancelProtocolCtx()

		// If we haven't opened the protocol, don't wait for it
		if pro != nil {
			if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
		}

		// If it hasn't sent any devices, the remote Silo peer doesn't send `EventCustomAllDevicesSent`
		// After the protocol has closed without errors, we can safely assume that we won't receive any
		// additional devices, so we mark all devices as received and ready
		select {
		case <-allRemoteDevicesReceived:
		default:
			signalAllRemoteDevicesReceived()

			// We need to call the hook manually too since we would otherwise only call if we received at least one device
			if hook := hooks.OnRemoteAllDevicesReceived; hook != nil {
				hook()
			}
		}

		signalAllRemoteDevicesReady()

		if hook := hooks.OnRemoteAllMigrationsCompleted; hook != nil {
			hook()
		}

		return nil
	})
	migratedPeer.Close = func() (errs error) {
		// We have to close the runner before we close the devices
		if err := peer.runner.Close(); err != nil {
			errs = errors.Join(errs, err)
		}

		defer func() {
			if err := migratedPeer.Wait(); err != nil {
				errs = errors.Join(errs, err)
			}
		}()

		deviceCloseFuncsLock.Lock()
		defer deviceCloseFuncsLock.Unlock()

		for _, closeFunc := range deviceCloseFuncs {
			defer func(closeFunc func() error) {
				if err := closeFunc(); err != nil {
					errs = errors.Join(errs, err)
				}
			}(closeFunc)
		}

		return
	}

	// We don't track this because we return the wait function
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := migratedPeer.Wait(); err != nil {
			panic(errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err))
		}
	})

	// We don't track this because we return the close function
	goroutineManager.StartBackgroundGoroutine(func(ctx context.Context) {
		select {
		// Failure case; we cancelled the internal context before all devices are ready
		case <-ctx.Done():
			if err := migratedPeer.Close(); err != nil {
				panic(errors.Join(ErrCouldNotCloseMigratedPeer, err))
			}

		// Happy case; all devices are ready and we want to wait with closing the devices until we stop the Firecracker process
		case <-allRemoteDevicesReady:
			<-peer.hypervisorCtx.Done()

			if err := migratedPeer.Close(); err != nil {
				panic(errors.Join(ErrCouldNotCloseMigratedPeer, err))
			}

			break
		}
	})

	select {
	case <-goroutineManager.Context().Done():
		if err := goroutineManager.Context().Err(); err != nil {
			panic(errors.Join(ErrPeerContextCancelled, err))
		}

		return
	case <-allRemoteDevicesReceived:
		break
	}

	//
	// Now deal with any local devices we want...
	//

	siloDevices := make([]*SiloDeviceConfig, 0)
	index := 0
	for _, input := range devices {
		if !slices.ContainsFunc(migratedPeer.stage2Inputs,
			func(r migrateFromStage) bool {
				return input.Name == r.name
			}) {
			if input.Shared {
				// Deal with shared devices here...
				exposedCb(index, input.Name, input.Base)
			} else {
				siloDevices = append(siloDevices, &SiloDeviceConfig{
					Id:        index,
					Name:      input.Name,
					Base:      input.Base,
					Overlay:   input.Overlay,
					State:     input.State,
					BlockSize: input.BlockSize,
				})
			}
			index++
		}
	}

	fmt.Printf("\n\nMigrateFromLocal %d devices\n\n", len(siloDevices))

	dg, err := SiloMigrateFromLocal(siloDevices)
	if err != nil {
		panic(errors.Join(mounter.ErrCouldNotSetupDevices, err))
	}

	// Save dg for later usage...
	migratedPeer.Dg = dg

	// Single shutdown function for deviceGroup
	deviceCloseFuncsLock.Lock()
	deviceCloseFuncs = append(deviceCloseFuncs, dg.CloseAll)
	deviceCloseFuncsLock.Unlock()

	// Go through dg and add inputs for next phases...
	for _, input := range siloDevices {
		stageOutputCb(migrateFromStage{
			name:   input.Name,
			id:     uint32(input.Id),
			remote: false,
		})

		dev := dg.GetExposedDeviceByName(input.Name)
		err = exposedCb(input.Id, input.Name, filepath.Join("/dev", dev.Device()))
		if err != nil {
			return nil, err
		}
	}

	if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
		hook()
	}

	select {
	case <-goroutineManager.Context().Done():
		if err := goroutineManager.Context().Err(); err != nil {
			panic(errors.Join(ErrPeerContextCancelled, err))
		}

		return
	case <-allRemoteDevicesReady:
		break
	}

	fmt.Printf("FINISHED MIGRATE FROM\n\n")
	for i, d := range migratedPeer.devices {
		fmt.Printf("migratedPeer.devices[%d] %v\n", i, d)
	}
	for i, d := range migratedPeer.stage2Inputs {
		fmt.Printf("migratedPeer.stage2Inputs[%d] %v\n", i, d)
	}
	fmt.Printf("VMPath %s\n", migratedPeer.runner.VMPath)

	return
}
