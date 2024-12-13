package peer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"golang.org/x/sys/unix"
)

type MigrateFromStage struct {
	Name      string
	Shared    bool
	Base      string
	Overlay   string
	State     string
	BlockSize uint32
}

func SiloMigrateFromLocal(devices []*MigrateFromStage, goroutineManager *manager.GoroutineManager, hooks mounter.MigrateFromHooks,
	stageOutputCb func(mfs migrateFromStage),
	vmPath string,
	addDeviceCloseFuncSingle func(f func() error),
	remainingRequestedLocalDevices *atomic.Int32,
) error {
	_, deferFuncs, err := utils.ConcurrentMap(
		devices,
		func(index int, input *MigrateFromStage, _ *struct{}, addDefer func(deferFunc func() error)) error {
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

				stageOutputCb(migrateFromStage{
					name: input.Name,

					blockSize: input.BlockSize,

					id:     uint32(index),
					remote: false,

					storage: local,
					device:  dev,
				})

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
				if err := unix.Mknod(filepath.Join(vmPath, input.Name), unix.S_IFBLK|0666, deviceID); err != nil {
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
			addDeviceCloseFuncSingle(deferFunc)
		}
	}

	return err
}
