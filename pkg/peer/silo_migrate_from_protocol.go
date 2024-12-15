package peer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/terminator"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type SiloFromDeviceInfo struct {
	Name string
	Base string
}

func SiloMigrateFrom(pro protocol.Protocol, eventHandler func(e *packets.Event)) (*devicegroup.DeviceGroup, error) {
	fmt.Printf("MigrateFrom...\n")
	tweak := func(index int, name string, schema string) string {
		s := strings.ReplaceAll(schema, "instance-0", "instance-1")
		fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
		return s
	}
	events := func(e *packets.Event) {
		fmt.Printf("Event received %v\n", e)
		eventHandler(e)
	}
	dg, err := devicegroup.NewFromProtocol(context.TODO(), pro, tweak, events, nil, nil)
	fmt.Printf("NewFromProtocol returned %v\n", err)

	dg.WaitForCompletion()

	return dg, err
}

func SiloMigrateFromGetInitDev(devices []*SiloFromDeviceInfo, goroutineManager *manager.GoroutineManager, hooks mounter.MigrateFromHooks,
	receivedButNotReadyRemoteDevices *atomic.Int32,
	protocolCtx context.Context,
	exposedCb func(index int, name string, devicePath string) error,
	addDeviceCloseFn func(f func() error),
	stageOutputCb func(migrateFromStage),
	eventHandler func(index uint32, e *packets.Event),
) func(ctx context.Context, p protocol.Protocol, index uint32) {

	return func(ctx context.Context, p protocol.Protocol, index uint32) {
		var (
			from  *protocol.FromProtocol
			local *waitingcache.Local
		)
		from = protocol.NewFromProtocol(
			ctx,
			index,
			func(di *packets.DevInfo) storage.Provider {
				// No need to `defer goroutineManager.HandlePanics` here - panics bubble upwards

				base := ""
				for _, device := range devices {
					if di.Name == device.Name {
						base = device.Base

						break
					}
				}

				if strings.TrimSpace(base) == "" {
					panic(terminator.ErrUnknownDeviceName)
				}

				receivedButNotReadyRemoteDevices.Add(1)

				if hook := hooks.OnRemoteDeviceReceived; hook != nil {
					hook(index, di.Name)
				}

				if err := os.MkdirAll(filepath.Dir(base), os.ModePerm); err != nil {
					panic(errors.Join(mounter.ErrCouldNotCreateDeviceDirectory, err))
				}

				src, dev, err := device.NewDevice(&config.DeviceSchema{
					Name:      di.Name,
					System:    "file",
					Location:  base,
					Size:      fmt.Sprintf("%v", di.Size),
					BlockSize: fmt.Sprintf("%v", di.BlockSize),
					Expose:    true,
				})
				if err != nil {
					panic(errors.Join(terminator.ErrCouldNotCreateDevice, err))
				}

				addDeviceCloseFn(dev.Shutdown)

				var remote *waitingcache.Remote
				local, remote = waitingcache.NewWaitingCache(src, int(di.BlockSize))
				local.NeedAt = func(offset int64, length int32) {
					// Only access the `from` protocol if it's not already closed
					select {
					case <-protocolCtx.Done():
						return

					default:
					}

					if err := from.NeedAt(offset, length); err != nil {
						panic(errors.Join(mounter.ErrCouldNotRequestBlock, err))
					}
				}
				local.DontNeedAt = func(offset int64, length int32) {
					// Only access the `from` protocol if it's not already closed
					select {
					case <-protocolCtx.Done():
						return

					default:
					}

					if err := from.DontNeedAt(offset, length); err != nil {
						panic(errors.Join(mounter.ErrCouldNotReleaseBlock, err))
					}
				}

				dev.SetProvider(local)

				stageOutputCb(migrateFromStage{
					name:    di.Name,
					id:      index,
					remote:  true,
					storage: local,
					device:  dev,
				})

				devicePath := filepath.Join("/dev", dev.Device())

				err = exposedCb(int(index), di.Name, devicePath)
				if err != nil {
					panic(err)
				}

				return remote
			},
			p,
		)

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := from.HandleReadAt(); err != nil {
				panic(errors.Join(terminator.ErrCouldNotHandleReadAt, err))
			}
		})

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := from.HandleWriteAt(); err != nil {
				panic(errors.Join(terminator.ErrCouldNotHandleWriteAt, err))
			}
		})

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := from.HandleDevInfo(); err != nil {
				panic(errors.Join(terminator.ErrCouldNotHandleDevInfo, err))
			}
		})

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := from.HandleEvent(func(e *packets.Event) {
				eventHandler(index, e)
			}); err != nil {
				panic(errors.Join(terminator.ErrCouldNotHandleEvent, err))
			}
		})

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := from.HandleDirtyList(func(blocks []uint) {
				if local != nil {
					local.DirtyBlocks(blocks)
				}
			}); err != nil {
				panic(errors.Join(terminator.ErrCouldNotHandleDirtyList, err))
			}
		})
	}

}
