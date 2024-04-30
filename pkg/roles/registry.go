package roles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	iconfig "github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type stage1 struct {
	name      string
	blockSize uint32
	base      string
}

type Device struct {
	prev stage1

	size        uint64
	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

type OpenDevicesHooks struct {
	OnDeviceOpened func(deviceID uint32, name string)
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
) ([]Device, []func() error, error) {
	stage1Inputs := []stage1{
		{
			name:      iconfig.StateName,
			blockSize: stateBlockSize,
			base:      statePath,
		},
		{
			name:      iconfig.MemoryName,
			blockSize: memoryBlockSize,
			base:      memoryPath,
		},
		{
			name:      iconfig.InitramfsName,
			blockSize: initramfsBlockSize,
			base:      initramfsPath,
		},
		{
			name:      iconfig.KernelName,
			blockSize: kernelBlockSize,
			base:      kernelPath,
		},
		{
			name:      iconfig.DiskName,
			blockSize: diskBlockSize,
			base:      diskPath,
		},
		{
			name:      iconfig.ConfigName,
			blockSize: configBlockSize,
			base:      configPath,
		},
	}

	devices, deferFuncs, err := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input stage1, output *Device, addDefer func(deferFunc func() error)) error {
			output.prev = input

			stat, err := os.Stat(input.base)
			if err != nil {
				return err
			}
			output.size = uint64(stat.Size())

			src, _, err := device.NewDevice(&config.DeviceSchema{
				Name:      input.name,
				System:    "file",
				Location:  input.base,
				Size:      fmt.Sprintf("%v", output.size),
				BlockSize: fmt.Sprintf("%v", input.blockSize),
				Expose:    false,
			})
			if err != nil {
				return err
			}
			addDefer(src.Close)

			metrics := modules.NewMetrics(src)
			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(input.blockSize))
			output.dirtyRemote = dirtyRemote
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.blockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			output.storage = storage
			addDefer(func() error {
				storage.Unlock()

				return nil
			})

			totalBlocks := (int(storage.Size()) + int(input.blockSize) - 1) / int(input.blockSize)
			output.totalBlocks = totalBlocks

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			output.orderer = orderer
			orderer.AddAll()

			if hook := hooks.OnDeviceOpened; hook != nil {
				hook(uint32(index), input.name)
			}

			return nil
		},
	)

	defers := []func() error{}
	for _, deferFuncs := range deferFuncs {
		defers = append(defers, deferFuncs...)
	}

	return devices, defers, err
}

type MigrateDevicesHooks struct {
	OnDeviceSent               func(deviceID uint32)
	OnDeviceAuthoritySent      func(deviceID uint32)
	OnDeviceMigrationProgress  func(deviceID uint32, ready int, total int)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

func MigrateDevices(
	ctx context.Context,

	devices []Device,
	concurrency int,

	readers []io.Reader,
	writers []io.Writer,

	hooks MigrateDevicesHooks,
) (errs error) {
	var errsLock sync.Mutex

	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errFinished)

	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
					errs = errors.Join(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	pro := protocol.NewProtocolRW(
		ctx,
		readers,
		writers,
		nil,
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}
	}()

	var devicesLeftToSend atomic.Int32

	_, deferFuncs, err := utils.ConcurrentMap(
		devices,
		func(index int, input Device, _ *struct{}, _ func(deferFunc func() error)) error {
			to := protocol.NewToProtocol(input.storage.Size(), uint32(index), pro)

			if err := to.SendDevInfo(input.prev.name, input.prev.blockSize); err != nil {
				return err
			}

			if hook := hooks.OnDeviceSent; hook != nil {
				hook(uint32(index))
			}

			devicesLeftToSend.Add(1)
			if devicesLeftToSend.Load() >= int32(len(devices)) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer handleGoroutinePanic()()

					if err := to.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(EventCustomAllDevicesSent),
					}); err != nil {
						panic(err)
					}

					if hook := hooks.OnAllDevicesSent; hook != nil {
						hook()
					}
				}()
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := to.HandleNeedAt(func(offset int64, length int32) {
					// Prioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(input.storage.Size()) {
						endOffset = uint64(input.storage.Size())
					}

					startBlock := int(offset / int64(input.prev.blockSize))
					endBlock := int((endOffset-1)/uint64(input.prev.blockSize)) + 1
					for b := startBlock; b < endBlock; b++ {
						input.orderer.PrioritiseBlock(b)
					}
				}); err != nil {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := to.HandleDontNeedAt(func(offset int64, length int32) {
					// Deprioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(input.storage.Size()) {
						endOffset = uint64(input.storage.Size())
					}

					startBlock := int(offset / int64(input.storage.Size()))
					endBlock := int((endOffset-1)/uint64(input.storage.Size())) + 1
					for b := startBlock; b < endBlock; b++ {
						input.orderer.Remove(b)
					}
				}); err != nil {
					panic(err)
				}
			}()

			cfg := migrator.NewMigratorConfig().WithBlockSize(int(input.prev.blockSize))
			cfg.Concurrency = map[int]int{
				storage.BlockTypeAny:      concurrency,
				storage.BlockTypeStandard: concurrency,
				storage.BlockTypeDirty:    concurrency,
				storage.BlockTypePriority: concurrency,
			}
			cfg.Progress_handler = func(p *migrator.MigrationProgress) {
				if hook := hooks.OnDeviceMigrationProgress; hook != nil {
					hook(uint32(index), p.Ready_blocks, p.Total_blocks)
				}
			}

			mig, err := migrator.NewMigrator(input.dirtyRemote, to, input.orderer, cfg)
			if err != nil {
				return err
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := to.SendEvent(&packets.Event{
					Type:       packets.EventCustom,
					CustomType: byte(EventCustomTransferAuthority),
				}); err != nil {
					panic(err)
				}

				if hook := hooks.OnDeviceAuthoritySent; hook != nil {
					hook(uint32(index))
				}
			}()

			if err := mig.Migrate(input.totalBlocks); err != nil {
				return err
			}

			if err := mig.WaitForCompletion(); err != nil {
				return err
			}

			if err := to.SendEvent(&packets.Event{
				Type: packets.EventCompleted,
			}); err != nil {
				return err
			}

			if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
				hook(uint32(index))
			}

			return nil
		},
	)

	if err != nil {
		panic(err)
	}

	for _, deferFuncs := range deferFuncs {
		for _, deferFunc := range deferFuncs {
			defer deferFunc()
		}
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
