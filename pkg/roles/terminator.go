package roles

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type CustomEventType byte

const (
	EventCustomAllDevicesSent    = CustomEventType(0)
	EventCustomTransferAuthority = CustomEventType(1)
)

var (
	ErrUnknownDeviceName = errors.New("unknown device name")
)

type TerminateHooks struct {
	OnDeviceReceived           func(deviceID uint32, name string)
	OnDeviceAuthorityReceived  func(deviceID uint32)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesReceived     func()
	OnAllMigrationsCompleted func()
}

func Terminate(
	ctx context.Context,

	statePath,
	memoryPath,
	initramfsPath,
	kernelPath,
	diskPath,
	configPath string,

	readers []io.Reader,
	writers []io.Writer,

	hooks TerminateHooks,
) (errs error) {
	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	var (
		deviceCloseFuncsLock sync.Mutex
		deviceCloseFuncs     []func() error
	)
	pro := protocol.NewProtocolRW(
		ctx,
		readers,
		writers,
		func(p protocol.Protocol, index uint32) {
			var (
				from  *protocol.FromProtocol
				local *waitingcache.WaitingCacheLocal
			)
			from = protocol.NewFromProtocol(
				index,
				func(di *packets.DevInfo) storage.StorageProvider {
					defer handlePanics(false)()

					var (
						path = ""
					)
					switch di.Name {
					case config.ConfigName:
						path = configPath

					case config.DiskName:
						path = diskPath

					case config.InitramfsName:
						path = initramfsPath

					case config.KernelName:
						path = kernelPath

					case config.MemoryName:
						path = memoryPath

					case config.StateName:
						path = statePath
					}

					if strings.TrimSpace(path) == "" {
						panic(ErrUnknownDeviceName)
					}

					if hook := hooks.OnDeviceReceived; hook != nil {
						hook(index, di.Name)
					}

					if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
						panic(err)
					}

					storage, err := sources.NewFileStorageCreate(path, int64(di.Size))
					if err != nil {
						panic(err)
					}

					deviceCloseFuncsLock.Lock()
					deviceCloseFuncs = append(deviceCloseFuncs, storage.Close) // defer storage.Close()
					deviceCloseFuncsLock.Unlock()

					var remote *waitingcache.WaitingCacheRemote
					local, remote = waitingcache.NewWaitingCache(storage, int(di.Block_size))
					local.NeedAt = func(offset int64, length int32) {
						if err := from.NeedAt(offset, length); err != nil {
							panic(err)
						}
					}
					local.DontNeedAt = func(offset int64, length int32) {
						if err := from.DontNeedAt(offset, length); err != nil {
							panic(err)
						}
					}

					return remote
				},
				p,
			)

			handleGoroutinePanics(true, func() {
				if err := from.HandleReadAt(); err != nil {
					panic(err)
				}
			})

			handleGoroutinePanics(true, func() {
				if err := from.HandleWriteAt(); err != nil {
					panic(err)
				}
			})

			handleGoroutinePanics(true, func() {
				if err := from.HandleDevInfo(); err != nil {
					panic(err)
				}
			})

			handleGoroutinePanics(true, func() {
				if err := from.HandleEvent(func(e *packets.Event) {
					switch e.Type {
					case packets.EventCustom:
						switch e.CustomType {
						case byte(EventCustomAllDevicesSent):
							if hook := hooks.OnAllDevicesReceived; hook != nil {
								hook()
							}

						case byte(EventCustomTransferAuthority):
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
					panic(err)
				}
			})

			handleGoroutinePanics(true, func() {
				if err := from.HandleDirtyList(func(blocks []uint) {
					if local != nil {
						local.DirtyBlocks(blocks)
					}
				}); err != nil {
					panic(err)
				}
			})
		})

	defer func() {
		defer handlePanics(true)()

		deviceCloseFuncsLock.Lock()
		defer deviceCloseFuncsLock.Unlock()

		for _, deferFunc := range deviceCloseFuncs {
			defer func(deferFunc func() error) {
				defer handlePanics(true)()

				if err := deferFunc(); err != nil {
					panic(err)
				}
			}(deferFunc)
		}
	}()

	if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
		panic(err)
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
