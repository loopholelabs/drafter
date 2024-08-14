package terminator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type TerminatorDevice struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

type TerminateHooks struct {
	OnDeviceReceived           func(deviceID uint32, name string)
	OnDeviceAuthorityReceived  func(deviceID uint32)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesReceived     func()
	OnAllMigrationsCompleted func()
}

func Terminate(
	ctx context.Context,

	devices []TerminatorDevice,

	readers []io.Reader,
	writers []io.Writer,

	hooks TerminateHooks,
) (errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	var (
		deviceCloseFuncsLock sync.Mutex
		deviceCloseFuncs     []func() error
	)
	pro := protocol.NewProtocolRW(
		goroutineManager.Context(),
		readers,
		writers,
		func(ctx context.Context, p protocol.Protocol, index uint32) {
			var (
				from  *protocol.FromProtocol
				local *waitingcache.WaitingCacheLocal
			)
			from = protocol.NewFromProtocol(
				ctx,
				index,
				func(di *packets.DevInfo) storage.StorageProvider {
					// No need to `defer goroutineManager.HandlePanics` here - panics bubble upwards

					path := ""
					for _, device := range devices {
						if di.Name == device.Name {
							path = device.Output

							break
						}
					}

					if strings.TrimSpace(path) == "" {
						panic(ErrUnknownDeviceName)
					}

					if hook := hooks.OnDeviceReceived; hook != nil {
						hook(index, di.Name)
					}

					if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
						panic(errors.Join(ErrCouldNotCreateReceivingDirectory, err))
					}

					src, device, err := device.NewDevice(&config.DeviceSchema{
						Name:      di.Name,
						System:    "file",
						Location:  path,
						Size:      fmt.Sprintf("%v", di.Size),
						BlockSize: fmt.Sprintf("%v", di.Block_size),
						Expose:    true,
					})
					if err != nil {
						panic(errors.Join(ErrCouldNotCreateDevice, err))
					}
					deviceCloseFuncsLock.Lock()
					deviceCloseFuncs = append(deviceCloseFuncs, device.Shutdown) // defer device.Shutdown()
					deviceCloseFuncsLock.Unlock()

					var remote *waitingcache.WaitingCacheRemote
					local, remote = waitingcache.NewWaitingCache(src, int(di.Block_size))
					local.NeedAt = func(offset int64, length int32) {
						// Only access the `from` protocol if it's not already closed
						select {
						case <-ctx.Done():
							return

						default:
						}

						if err := from.NeedAt(offset, length); err != nil {
							panic(errors.Join(ErrCouldNotSendNeedAt, err))
						}
					}
					local.DontNeedAt = func(offset int64, length int32) {
						// Only access the `from` protocol if it's not already closed
						select {
						case <-ctx.Done():
							return

						default:
						}

						if err := from.DontNeedAt(offset, length); err != nil {
							panic(errors.Join(ErrCouldNotSendDontNeedAt, err))
						}
					}

					device.SetProvider(local)

					return remote
				},
				p,
			)

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := from.HandleReadAt(); err != nil {
					panic(errors.Join(ErrCouldNotHandleReadAt, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := from.HandleWriteAt(); err != nil {
					panic(errors.Join(ErrCouldNotHandleWriteAt, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := from.HandleDevInfo(); err != nil {
					panic(errors.Join(ErrCouldNotHandleDevInfo, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := from.HandleEvent(func(e *packets.Event) {
					switch e.Type {
					case packets.EventCustom:
						switch e.CustomType {
						case byte(registry.EventCustomAllDevicesSent):
							if hook := hooks.OnAllDevicesReceived; hook != nil {
								hook()
							}

						case byte(registry.EventCustomTransferAuthority):
							if hook := hooks.OnDeviceAuthorityReceived; hook != nil {
								hook(index)
							}
						}

					case packets.EventCompleted:
						if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
							hook(index)
						}
					}
				}); err != nil {
					panic(errors.Join(ErrCouldNotHandleEvent, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := from.HandleDirtyList(func(blocks []uint) {
					if local != nil {
						local.DirtyBlocks(blocks)
					}
				}); err != nil {
					panic(errors.Join(ErrCouldNotHandleDirtyList, err))
				}
			})
		})

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		deviceCloseFuncsLock.Lock()
		defer deviceCloseFuncsLock.Unlock()

		for _, closeFunc := range deviceCloseFuncs {
			defer func(closeFunc func() error) {
				defer goroutineManager.CreateForegroundPanicCollector()()

				if err := closeFunc(); err != nil {
					panic(errors.Join(ErrCouldNotCloseDevice, err))
				}
			}(closeFunc)
		}
	}()

	if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
		panic(errors.Join(registry.ErrCouldNotHandleProtocol, err))
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
