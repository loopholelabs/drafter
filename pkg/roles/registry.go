package roles

import (
	"fmt"
	"os"
	"time"

	iconfig "github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

// TODO: Make these private once both funcs are implemented
type stage1 struct {
	Name      string
	BlockSize uint32
	Base      string
}

// TODO: Make these private once both funcs are implemented
type Device struct {
	Prev stage1

	Size        uint64
	Storage     *modules.Lockable
	Orderer     *blocks.PriorityBlockOrder
	TotalBlocks int
	DirtyRemote *dirtytracker.DirtyTrackerRemote
}

type OpenDevicesHooks struct {
	OnOpenDevice func(deviceID uint32, name string)
}

func OpenDevices(
	statePath,
	memoryPath,
	initramfsPath,
	kernelPath,
	diskPath,
	configPath string,

	stateBlockSize,
	memoryBlockSize,
	initramfsBlockSize,
	kernelBlockSize,
	diskBlockSize,
	configBlockSize uint32,

	hooks OpenDevicesHooks,
) ([]Device, []func() error, []error) {
	stage1Inputs := []stage1{
		{
			Name:      iconfig.StateName,
			BlockSize: stateBlockSize,
			Base:      statePath,
		},
		{
			Name:      iconfig.MemoryName,
			BlockSize: memoryBlockSize,
			Base:      memoryPath,
		},
		{
			Name:      iconfig.InitramfsName,
			BlockSize: initramfsBlockSize,
			Base:      initramfsPath,
		},
		{
			Name:      iconfig.KernelName,
			BlockSize: kernelBlockSize,
			Base:      kernelPath,
		},
		{
			Name:      iconfig.DiskName,
			BlockSize: diskBlockSize,
			Base:      diskPath,
		},
		{
			Name:      iconfig.ConfigName,
			BlockSize: configBlockSize,
			Base:      configPath,
		},
	}

	devices, rawDefers, errs := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input stage1, output *Device, addDefer func(deferFunc func() error)) error {
			output.Prev = input

			if hook := hooks.OnOpenDevice; hook != nil {
				hook(uint32(index), input.Name)
			}

			stat, err := os.Stat(input.Base)
			if err != nil {
				return err
			}
			output.Size = uint64(stat.Size())

			src, _, err := device.NewDevice(&config.DeviceSchema{
				Name:      input.Name,
				System:    "file",
				Location:  input.Base,
				Size:      fmt.Sprintf("%v", output.Size),
				BlockSize: fmt.Sprintf("%v", input.BlockSize),
				Expose:    false,
			})
			if err != nil {
				return err
			}
			addDefer(src.Close)

			metrics := modules.NewMetrics(src)
			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(input.BlockSize))
			output.DirtyRemote = dirtyRemote
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.BlockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			output.Storage = storage
			addDefer(func() error {
				storage.Unlock()

				return nil
			})

			totalBlocks := (int(storage.Size()) + int(input.BlockSize) - 1) / int(input.BlockSize)
			output.TotalBlocks = totalBlocks

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			output.Orderer = orderer
			orderer.AddAll()

			return nil
		},
	)

	defers := []func() error{}
	for _, rawDefer := range rawDefers {
		defers = append(defers, rawDefer...)
	}

	return devices, defers, errs
}
