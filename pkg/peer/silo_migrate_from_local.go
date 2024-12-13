package peer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
)

type MigrateFromStage struct {
	Id        int
	Name      string
	Base      string
	Overlay   string
	State     string
	BlockSize uint32
}

func SiloMigrateFromLocal(devices []*MigrateFromStage, goroutineManager *manager.GoroutineManager, hooks mounter.MigrateFromHooks,
	stageOutputCb func(mfs migrateFromStage),
	addDeviceCloseFuncSingle func(f func() error),
	exposedCb func(index int, name string, devicePath string) error,
) error {
	// Use an atomic counter instead of a WaitGroup so that we can wait without leaking a goroutine
	var remainingRequestedLocalDevices atomic.Int32
	remainingRequestedLocalDevices.Add(int32(len(devices)))

	_, deferFuncs, err := utils.ConcurrentMap(
		devices,
		func(_ int, input *MigrateFromStage, _ *struct{}, addDefer func(deferFunc func() error)) error {
			if hook := hooks.OnLocalDeviceRequested; hook != nil {
				hook(uint32(input.Id), input.Name)
			}

			if remainingRequestedLocalDevices.Add(-1) <= 0 {
				if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
					hook()
				}
			}

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
				name:      input.Name,
				blockSize: input.BlockSize,
				id:        uint32(input.Id),
				remote:    false,
				storage:   local,
				device:    dev,
			})

			devicePath := filepath.Join("/dev", dev.Device())

			return exposedCb(input.Id, input.Name, devicePath)

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
