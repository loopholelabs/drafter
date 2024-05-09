package roles

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type MigrateFromHooks struct {
	OnDeviceReceived           func(deviceID uint32, name string)
	OnDeviceAuthorityReceived  func(deviceID uint32)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesReceived     func()
	OnAllMigrationsCompleted func()
}

type Peer struct {
	VMPath string

	Wait  func() error
	Close func() error

	MigrateFrom func(
		ctx context.Context,

		statePath,
		memoryPath,
		initramfsPath,
		kernelPath,
		diskPath,
		configPath string,

		readers []io.Reader,
		writers []io.Writer,

		hooks MigrateFromHooks,

		resumeTimeout time.Duration,
	) (errs error)
}

func StartPeer(
	hypervisorCtx context.Context,
	rescueCtx context.Context,
	hypervisorConfiguration config.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	peer *Peer,

	errs error,
) {
	peer = &Peer{}

	_, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		hypervisorCtx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	runner, err := StartRunner(
		hypervisorCtx,
		rescueCtx,

		hypervisorConfiguration,

		stateName,
		memoryName,
	)

	// We set both of these even if we return an error since we need to have a way to wait for rescue operations to complete
	peer.Wait = runner.Wait
	peer.Close = func() error {
		if runner.Close != nil {
			if err := runner.Close(); err != nil {
				return err
			}
		}

		if peer.Wait != nil {
			if err := peer.Wait(); err != nil {
				return err
			}
		}

		return nil
	}

	if err != nil {
		panic(err)
	}

	// We don't track this because we return the wait function
	handleGoroutinePanics(false, func() {
		if err := runner.Wait(); err != nil {
			panic(err)
		}
	})

	peer.MigrateFrom = func(ctx context.Context, statePath, memoryPath, initramfsPath, kernelPath, diskPath, configPath string, readers []io.Reader, writers []io.Writer, hooks MigrateFromHooks, resumeTimeout time.Duration) (errs error) {
		ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
			ctx,
			&errs,
			utils.GetPanicHandlerHooks{},
		)
		defer wait()
		defer cancel()
		defer handlePanics(false)()

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
							case byte(EventCustomTransferAuthority):
								if hook := hooks.OnDeviceAuthorityReceived; hook != nil {
									hook(index)
								}

							case byte(EventCustomAllDevicesSent):
								if hook := hooks.OnAllDevicesReceived; hook != nil {
									hook()
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

		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}

		if hook := hooks.OnAllMigrationsCompleted; hook != nil {
			hook()
		}

		return
	}

	return
}
