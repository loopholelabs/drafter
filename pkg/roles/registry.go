package roles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	iutils "github.com/loopholelabs/drafter/internal/utils"
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

var (
	ErrCouldNotContinueWithMigration = errors.New("could not continue with migration")
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
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

type OpenDevicesHooks struct {
	OnDeviceOpened func(deviceID uint32, name string)
}

func OpenDevices(
	devices []RegistryDevice,

	hooks OpenDevicesHooks,
) ([]OpenedRegistryDevice, []func() error, error) {
	openedDevices, deferFuncs, err := iutils.ConcurrentMap(
		devices,
		func(index int, input RegistryDevice, output *OpenedRegistryDevice, addDefer func(deferFunc func() error)) error {
			output.RegistryDevice = input

			stat, err := os.Stat(input.Input)
			if err != nil {
				return err
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
				return err
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

type MigrateDevicesHooks struct {
	OnDeviceSent               func(deviceID uint32)
	OnDeviceAuthoritySent      func(deviceID uint32)
	OnDeviceMigrationProgress  func(deviceID uint32, ready int, total int)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

func MigrateOpenedDevices(
	ctx context.Context,

	openedDevices []OpenedRegistryDevice,
	concurrency int,

	readers []io.Reader,
	writers []io.Writer,

	hooks MigrateDevicesHooks,
) (errs error) {
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
		nil,
	)

	handleGoroutinePanics(true, func() {
		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}
	})

	var devicesLeftToSend atomic.Int32

	_, deferFuncs, err := iutils.ConcurrentMap(
		openedDevices,
		func(index int, input OpenedRegistryDevice, _ *struct{}, _ func(deferFunc func() error)) error {
			to := protocol.NewToProtocol(input.storage.Size(), uint32(index), pro)

			if err := to.SendDevInfo(input.RegistryDevice.Name, input.RegistryDevice.BlockSize, ""); err != nil {
				return err
			}

			if hook := hooks.OnDeviceSent; hook != nil {
				hook(uint32(index))
			}

			devicesLeftToSend.Add(1)
			if devicesLeftToSend.Load() >= int32(len(openedDevices)) {
				handleGoroutinePanics(true, func() {
					if err := to.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(EventCustomAllDevicesSent),
					}); err != nil {
						panic(err)
					}

					if hook := hooks.OnAllDevicesSent; hook != nil {
						hook()
					}
				})
			}

			handleGoroutinePanics(true, func() {
				if err := to.HandleNeedAt(func(offset int64, length int32) {
					// Prioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(input.storage.Size()) {
						endOffset = uint64(input.storage.Size())
					}

					startBlock := int(offset / int64(input.RegistryDevice.BlockSize))
					endBlock := int((endOffset-1)/uint64(input.RegistryDevice.BlockSize)) + 1
					for b := startBlock; b < endBlock; b++ {
						input.orderer.PrioritiseBlock(b)
					}
				}); err != nil {
					panic(err)
				}
			})

			handleGoroutinePanics(true, func() {
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
			})

			cfg := migrator.NewMigratorConfig().WithBlockSize(int(input.RegistryDevice.BlockSize))
			cfg.Concurrency = map[int]int{
				storage.BlockTypeAny:      concurrency,
				storage.BlockTypeStandard: concurrency,
				storage.BlockTypeDirty:    concurrency,
				storage.BlockTypePriority: concurrency,
			}
			cfg.Locker_handler = func() {
				defer handlePanics(false)()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreLock,
				}); err != nil {
					panic(err)
				}

				input.storage.Lock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostLock,
				}); err != nil {
					panic(err)
				}
			}
			cfg.Unlocker_handler = func() {
				defer handlePanics(false)()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreUnlock,
				}); err != nil {
					panic(err)
				}

				input.storage.Unlock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostUnlock,
				}); err != nil {
					panic(err)
				}
			}
			cfg.Error_handler = func(b *storage.BlockInfo, err error) {
				defer handlePanics(false)()

				if err != nil {
					panic(errors.Join(ErrCouldNotContinueWithMigration, err))
				}
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

			handleGoroutinePanics(true, func() {
				if err := to.SendEvent(&packets.Event{
					Type:       packets.EventCustom,
					CustomType: byte(EventCustomTransferAuthority),
				}); err != nil {
					panic(err)
				}

				if hook := hooks.OnDeviceAuthoritySent; hook != nil {
					hook(uint32(index))
				}
			})

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
			defer deferFunc() // We can safely ignore errors here since we never call `addDefer` with a function that could return an error
		}
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
