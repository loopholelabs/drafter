package peer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
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

func SiloCreateDevSchema(i *MigrateFromStage) (*config.DeviceSchema, error) {
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

func SiloMigrateFromLocal(devices []*MigrateFromStage, goroutineManager *manager.GoroutineManager, hooks mounter.MigrateFromHooks,
	stageOutputCb func(mfs migrateFromStage),
	addDeviceCloseFuncSingle func(f func() error),
	exposedCb func(index int, name string, devicePath string) error,
) error {

	// First create a set of schema for the devices...
	siloDevices := make([]*config.DeviceSchema, 0)
	for _, i := range devices {
		ds, err := SiloCreateDevSchema(i)
		if err != nil {
			return err
		}
		siloDevices = append(siloDevices, ds)
		fmt.Printf("%s\n", ds.EncodeAsBlock())
	}

	// Create the devices...
	for index, input := range devices {
		if hook := hooks.OnLocalDeviceRequested; hook != nil {
			hook(uint32(input.Id), input.Name)
		}

		ds := siloDevices[index]
		local, dev, err := device.NewDevice(ds)

		if err != nil {
			return errors.Join(mounter.ErrCouldNotCreateLocalDevice, err)
		}
		addDeviceCloseFuncSingle(local.Close)
		addDeviceCloseFuncSingle(dev.Shutdown)

		stageOutputCb(migrateFromStage{
			name:      input.Name,
			blockSize: input.BlockSize,
			id:        uint32(input.Id),
			remote:    false,
			storage:   local,
			device:    dev,
		})

		err = exposedCb(input.Id, input.Name, filepath.Join("/dev", dev.Device()))
		if err != nil {
			return err
		}
	}

	if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
		hook()
	}

	return nil
}
