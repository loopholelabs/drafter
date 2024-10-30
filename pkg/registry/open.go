package registry

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type RegistryDevice struct {
	Name      string `json:"name"`
	Input     string `json:"input"`
	BlockSize uint32 `json:"blockSize"`
}

type OpenedRegistryDevice struct {
	RegistryDevice RegistryDevice

	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.Remote
}

type OpenDevicesHooks struct {
	OnDeviceOpened func(deviceID uint32, name string)
}

func OpenDevices(
	devices []RegistryDevice,

	hooks OpenDevicesHooks,
) ([]OpenedRegistryDevice, []func() error, error) {
	openedDevices, deferFuncs, err := utils.ConcurrentMap(
		devices,
		func(index int, input RegistryDevice, output *OpenedRegistryDevice, addDefer func(deferFunc func() error)) error {
			output.RegistryDevice = input

			stat, err := os.Stat(input.Input)
			if err != nil {
				return errors.Join(ErrCouldNotGetInputDeviceStatistics, err)
			}

			src, _, err := device.NewDevice(&config.DeviceSchema{
				Name:      input.Name,
				System:    "file",
				Location:  input.Input,
				Size:      fmt.Sprintf("%v", stat.Size()),
				BlockSize: fmt.Sprintf("%v", input.BlockSize),
				Expose:    false,
			})
			if err != nil {
				return errors.Join(ErrCouldNotCreateNewDevice, err)
			}
			addDefer(src.Close)

			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(src, int(input.BlockSize))
			output.dirtyRemote = dirtyRemote
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.BlockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			output.storage = storage
			addDefer(func() error {
				storage.Unlock()

				return nil
			})

			totalBlocks := (int(storage.Size()) + int(input.BlockSize) - 1) / int(input.BlockSize)
			output.totalBlocks = totalBlocks

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			output.orderer = orderer
			orderer.AddAll()

			if hook := hooks.OnDeviceOpened; hook != nil {
				hook(uint32(index), input.Name)
			}

			return nil
		},
	)

	defers := []func() error{}
	for _, deferFuncs := range deferFuncs {
		defers = append(defers, deferFuncs...)
	}

	return openedDevices, defers, err
}
