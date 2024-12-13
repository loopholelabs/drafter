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

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"golang.org/x/sys/unix"
)

type MigrateFromDevice[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Name string `json:"name"`

	Base    string `json:"base"`
	Overlay string `json:"overlay"`
	State   string `json:"state"`

	BlockSize uint32 `json:"blockSize"`

	Shared bool `json:"shared"`
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

		devices: devices,
		runner:  peer.runner,

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

	di := make([]*SiloFromDeviceInfo, 0)
	for _, dev := range devices {
		di = append(di, &SiloFromDeviceInfo{
			Name: dev.Name,
			Base: dev.Base,
		})
	}

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

	initDev := SiloMigrateFromGetInitDev(di, goroutineManager, hooks, &receivedButNotReadyRemoteDevices, protocolCtx,
		signalAllRemoteDevicesReady,
		signalAllRemoteDevicesReceived,
		peer.runner.VMPath,
		addDeviceCloseFunc,
		stageOutputCb,
	)

	if len(readers) > 0 && len(writers) > 0 { // Only open the protocol if we want passed in readers and writers
		pro = protocol.NewRW(
			protocolCtx, // We don't track this because we return the wait function
			readers,
			writers,
			initDev)
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

	stage1Inputs := []MigrateFromDevice[L, R, G]{}
	for _, input := range devices {
		if slices.ContainsFunc(
			migratedPeer.stage2Inputs,
			func(r migrateFromStage) bool {
				return input.Name == r.name
			},
		) {
			continue
		}

		stage1Inputs = append(stage1Inputs, input)
	}

	// Use an atomic counter instead of a WaitGroup so that we can wait without leaking a goroutine
	var remainingRequestedLocalDevices atomic.Int32
	remainingRequestedLocalDevices.Add(int32(len(stage1Inputs)))

	_, deferFuncs, err := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input MigrateFromDevice[L, R, G], _ *struct{}, addDefer func(deferFunc func() error)) error {
			if hook := hooks.OnLocalDeviceRequested; hook != nil {
				hook(uint32(index), input.Name)
			}

			if remainingRequestedLocalDevices.Add(-1) <= 0 {
				if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
					hook()
				}
			}

			devicePath := ""
			if input.Shared {
				devicePath = input.Base
			} else {
				stat, err := os.Stat(input.Base)
				if err != nil {
					return errors.Join(mounter.ErrCouldNotGetBaseDeviceStat, err)
				}

				var (
					local storage.Provider
					dev   storage.ExposedStorage
				)
				if strings.TrimSpace(input.Overlay) == "" || strings.TrimSpace(input.State) == "" {
					local, dev, err = device.NewDevice(&config.DeviceSchema{
						Name:      input.Name,
						System:    "file",
						Location:  input.Base,
						Size:      fmt.Sprintf("%v", stat.Size()),
						BlockSize: fmt.Sprintf("%v", input.BlockSize),
						Expose:    true,
					})
				} else {
					if err := os.MkdirAll(filepath.Dir(input.Overlay), os.ModePerm); err != nil {
						return errors.Join(mounter.ErrCouldNotCreateOverlayDirectory, err)
					}

					if err := os.MkdirAll(filepath.Dir(input.State), os.ModePerm); err != nil {
						return errors.Join(mounter.ErrCouldNotCreateStateDirectory, err)
					}

					local, dev, err = device.NewDevice(&config.DeviceSchema{
						Name:      input.Name,
						System:    "sparsefile",
						Location:  input.Overlay,
						Size:      fmt.Sprintf("%v", stat.Size()),
						BlockSize: fmt.Sprintf("%v", input.BlockSize),
						Expose:    true,
						ROSource: &config.DeviceSchema{
							Name:     input.State,
							System:   "file",
							Location: input.Base,
							Size:     fmt.Sprintf("%v", stat.Size()),
						},
					})
				}
				if err != nil {
					return errors.Join(mounter.ErrCouldNotCreateLocalDevice, err)
				}
				addDefer(local.Close)
				addDefer(dev.Shutdown)

				dev.SetProvider(local)

				stage2InputsLock.Lock()
				migratedPeer.stage2Inputs = append(migratedPeer.stage2Inputs, migrateFromStage{
					name: input.Name,

					blockSize: input.BlockSize,

					id:     uint32(index),
					remote: false,

					storage: local,
					device:  dev,
				})
				stage2InputsLock.Unlock()

				devicePath = filepath.Join("/dev", dev.Device())
			}

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
				if err := unix.Mknod(filepath.Join(peer.runner.VMPath, input.Name), unix.S_IFBLK|0666, deviceID); err != nil {
					return errors.Join(ErrCouldNotCreateDeviceNode, err)
				}
			}

			if hook := hooks.OnLocalDeviceExposed; hook != nil {
				hook(uint32(index), devicePath)
			}

			return nil
		},
	)

	// Make sure that we schedule the `deferFuncs` even if we get an error during device setup
	for _, deferFuncs := range deferFuncs {
		for _, deferFunc := range deferFuncs {
			deviceCloseFuncsLock.Lock()
			deviceCloseFuncs = append(deviceCloseFuncs, deferFunc) // defer deferFunc()
			deviceCloseFuncsLock.Unlock()
		}
	}

	if err != nil {
		panic(errors.Join(mounter.ErrCouldNotSetupDevices, err))
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

	return
}
