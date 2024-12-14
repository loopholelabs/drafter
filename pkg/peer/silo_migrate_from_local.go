package peer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type SiloDeviceConfig struct {
	Id        int
	Name      string
	Base      string
	Overlay   string
	State     string
	BlockSize uint32
}

func SiloCreateDevSchema(i *SiloDeviceConfig) (*config.DeviceSchema, error) {
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

func SiloMigrateFromLocal(devices []*SiloDeviceConfig) (*devicegroup.DeviceGroup, error) {

	// First create a set of schema for the devices...
	siloDevices := make([]*config.DeviceSchema, 0)
	for _, i := range devices {
		ds, err := SiloCreateDevSchema(i)
		if err != nil {
			return nil, err
		}
		siloDevices = append(siloDevices, ds)
		fmt.Printf("%s\n", ds.EncodeAsBlock())
	}

	// Create a deviceGroup from all the schemas
	dg, err := devicegroup.NewFromSchema(siloDevices, nil, nil)
	if err != nil {
		return nil, errors.Join(mounter.ErrCouldNotCreateLocalDevice, err)
	}

	return dg, nil
}
