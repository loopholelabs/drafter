package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"golang.org/x/sys/unix"
)

// expose a Silo Device as a file within the vm directory
func exposeSiloDeviceAsFile(vmpath string, name string, devicePath string) error {
	deviceInfo, err := os.Stat(devicePath)
	if err != nil {
		return errors.Join(snapshotter.ErrCouldNotGetDeviceStat, err)
	}

	deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrCouldNotGetNBDDeviceStat
	}

	err = unix.Mknod(filepath.Join(vmpath, name), unix.S_IFBLK|0666, int(deviceStat.Rdev))
	if err != nil {
		return errors.Join(ErrCouldNotCreateDeviceNode, err)
	}

	return nil
}

/**
 * This creates a Silo Dev Schema given a MigrateFromDevice
 * If you want to change the type of storage used, or Silo options, you can do so here.
 *
 */
func createSiloDevSchema(i *MigrateFromDevice) (*config.DeviceSchema, error) {
	stat, err := os.Stat(i.Base)
	if err != nil {
		return nil, errors.Join(mounter.ErrCouldNotGetBaseDeviceStat, err)
	}

	ds := &config.DeviceSchema{
		Name:      i.Name,
		BlockSize: fmt.Sprintf("%v", i.BlockSize),
		Expose:    true,
		Size:      fmt.Sprintf("%v", stat.Size()),
	}
	if strings.TrimSpace(i.Overlay) == "" || strings.TrimSpace(i.State) == "" {
		ds.System = "file"
		ds.Location = i.Base
	} else {
		err := os.MkdirAll(filepath.Dir(i.Overlay), os.ModePerm)
		if err != nil {
			return nil, errors.Join(mounter.ErrCouldNotCreateOverlayDirectory, err)
		}

		err = os.MkdirAll(filepath.Dir(i.State), os.ModePerm)
		if err != nil {
			return nil, errors.Join(mounter.ErrCouldNotCreateStateDirectory, err)
		}

		ds.System = "sparsefile"
		ds.Location = i.Overlay

		ds.ROSource = &config.DeviceSchema{
			Name:     i.State,
			System:   "file",
			Location: i.Base,
			Size:     fmt.Sprintf("%v", stat.Size()),
		}
	}
	return ds, nil
}

/**
 * 'migrate' from the local filesystem.
 *
 */
func migrateFromFS(vmpath string, devices []MigrateFromDevice) (*devicegroup.DeviceGroup, error) {
	siloDeviceSchemas := make([]*config.DeviceSchema, 0)
	for _, input := range devices {
		if input.Shared {
			// Deal with shared devices here...
			err := exposeSiloDeviceAsFile(vmpath, input.Name, input.Base)
			if err != nil {
				return nil, err
			}
		} else {
			ds, err := createSiloDevSchema(&input)
			if err != nil {
				return nil, err
			}
			siloDeviceSchemas = append(siloDeviceSchemas, ds)
		}
	}

	// Create a silo deviceGroup from all the schemas
	dg, err := devicegroup.NewFromSchema(siloDeviceSchemas, nil, nil)
	if err != nil {
		return nil, err
	}

	for _, input := range siloDeviceSchemas {
		dev := dg.GetExposedDeviceByName(input.Name)
		err = exposeSiloDeviceAsFile(vmpath, input.Name, filepath.Join("/dev", dev.Device()))
		if err != nil {
			return nil, err
		}
	}
	return dg, nil
}

///////////////////////////////////////////////////////////////////////////////
// Under here is still WIP

type MigrateFromDevice struct {
	Name      string `json:"name"`
	Base      string `json:"base"`
	Overlay   string `json:"overlay"`
	State     string `json:"state"`
	BlockSize uint32 `json:"blockSize"`
	Shared    bool   `json:"shared"`
}

func (peer *Peer[L, R, G]) MigrateFrom(
	ctx context.Context,
	devices []MigrateFromDevice,
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
		devices: devices,
		runner:  peer.runner,
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
		deviceCloseFuncsLock sync.Mutex
		deviceCloseFuncs     []func() error
		pro                  *protocol.RW
	)

	addDeviceCloseFunc := func(f func() error) {
		deviceCloseFuncsLock.Lock()
		deviceCloseFuncs = append(deviceCloseFuncs, f)
		deviceCloseFuncs = append(deviceCloseFuncs, peer.runner.Close) // defer runner.Close()
		deviceCloseFuncsLock.Unlock()
	}

	if len(readers) > 0 && len(writers) > 0 { // Only open the protocol if we want passed in readers and writers
		pro = protocol.NewRW(protocolCtx, readers, writers, nil)

		// Do this in a goroutine for now...
		go func() {
			fmt.Printf("MigrateFrom...\n")
			// For now...
			names := make([]string, 0)
			var namesLock sync.Mutex
			tweak := func(index int, name string, schema string) string {
				namesLock.Lock()
				names = append(names, name)
				namesLock.Unlock()

				s := strings.ReplaceAll(schema, "instance-0", "instance-1")
				fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
				return string(s)
			}
			events := func(e *packets.Event) {}
			dg, err := devicegroup.NewFromProtocol(context.TODO(), pro, tweak, events, nil, nil)
			if err != nil {
				fmt.Printf("Error migrating %v\n", err)
			}

			// TODO: Setup goroutine better etc etc
			go func() {
				err := dg.HandleCustomData(func(data []byte) {
					fmt.Printf("\n\nCustomData %v\n\n", data)
					if len(data) == 1 && data[0] == byte(registry.EventCustomTransferAuthority) {
						signalAllRemoteDevicesReady()
					}
				})
				if err != nil {
					fmt.Printf("HandleCustomData returned %v\n", err)
				}
			}()

			for _, n := range names {
				dev := dg.GetExposedDeviceByName(n)
				if dev != nil {
					err := exposeSiloDeviceAsFile(migratedPeer.runner.VMPath, n, filepath.Join("/dev", dev.Device()))
					if err != nil {
						fmt.Printf("Error migrating %v\n", err)
					}
				}
			}

			dg.WaitForCompletion()

			// Save dg for future migrations.
			migratedPeer.Dg = dg
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

		// FIXME: We need to wait until all devices are COMPLETE here...

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

	select {
	case <-goroutineManager.Context().Done():
		if err := goroutineManager.Context().Err(); err != nil {
			panic(errors.Join(ErrPeerContextCancelled, err))
		}

		return
	case <-allRemoteDevicesReady:
		break
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		dg, err := migrateFromFS(migratedPeer.runner.VMPath, devices)
		if err != nil {
			panic(err)
		}

		// Save dg for later usage, when we want to migrate from here etc
		migratedPeer.Dg = dg

		// Single shutdown function for the deviceGroup
		deviceCloseFuncsLock.Lock()
		deviceCloseFuncs = append(deviceCloseFuncs, dg.CloseAll)
		deviceCloseFuncsLock.Unlock()
		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	return
}
