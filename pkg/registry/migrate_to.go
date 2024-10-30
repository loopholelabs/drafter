package registry

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
)

type MigrateToHooks struct {
	OnDeviceSent               func(deviceID uint32)
	OnDeviceAuthoritySent      func(deviceID uint32)
	OnDeviceMigrationProgress  func(deviceID uint32, ready int, total int)
	OnDeviceMigrationCompleted func(deviceID uint32)

	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

func MigrateTo(
	ctx context.Context,

	openedDevices []OpenedRegistryDevice,
	concurrency int,

	readers []io.Reader,
	writers []io.Writer,

	hooks MigrateToHooks,
) (errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	pro := protocol.NewRW(
		goroutineManager.Context(),
		readers,
		writers,
		nil,
	)

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(errors.Join(ErrCouldNotHandleProtocol, err))
		}
	})

	var devicesLeftToSend atomic.Int32

	_, deferFuncs, err := utils.ConcurrentMap(
		openedDevices,
		func(index int, input OpenedRegistryDevice, _ *struct{}, _ func(deferFunc func() error)) error {
			to := protocol.NewToProtocol(input.storage.Size(), uint32(index), pro)

			if err := to.SendDevInfo(input.RegistryDevice.Name, input.RegistryDevice.BlockSize, ""); err != nil {
				return errors.Join(ErrCouldNotSendDeviceInfo, err)
			}

			if hook := hooks.OnDeviceSent; hook != nil {
				hook(uint32(index))
			}

			devicesLeftToSend.Add(1)
			if devicesLeftToSend.Load() >= int32(len(openedDevices)) {
				goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
					if err := to.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(EventCustomAllDevicesSent),
					}); err != nil {
						panic(errors.Join(ErrCouldNotSendEvent, err))
					}

					if hook := hooks.OnAllDevicesSent; hook != nil {
						hook()
					}
				})
			}

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
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
					panic(errors.Join(ErrCouldNotHandleNeedAt, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
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
					panic(errors.Join(ErrCouldNotHandleDontNeedAt, err))
				}
			})

			cfg := migrator.NewConfig().WithBlockSize(int(input.RegistryDevice.BlockSize))
			cfg.Concurrency = map[int]int{
				storage.BlockTypeAny:      concurrency,
				storage.BlockTypeStandard: concurrency,
				storage.BlockTypeDirty:    concurrency,
				storage.BlockTypePriority: concurrency,
			}
			cfg.LockerHandler = func() {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreLock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendEvent, err))
				}

				input.storage.Lock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostLock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendEvent, err))
				}
			}
			cfg.UnlockerHandler = func() {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreUnlock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendEvent, err))
				}

				input.storage.Unlock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostUnlock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendEvent, err))
				}
			}
			cfg.ErrorHandler = func(b *storage.BlockInfo, err error) {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err != nil {
					panic(errors.Join(ErrCouldNotContinueWithMigration, err))
				}
			}
			cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
				if hook := hooks.OnDeviceMigrationProgress; hook != nil {
					hook(uint32(index), p.ReadyBlocks, p.TotalBlocks)
				}
			}

			mig, err := migrator.NewMigrator(input.dirtyRemote, to, input.orderer, cfg)
			if err != nil {
				return errors.Join(ErrCouldNotCreateMigrator, err)
			}

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := to.SendEvent(&packets.Event{
					Type:       packets.EventCustom,
					CustomType: byte(EventCustomTransferAuthority),
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendEvent, err))
				}

				if hook := hooks.OnDeviceAuthoritySent; hook != nil {
					hook(uint32(index))
				}
			})

			if err := mig.Migrate(input.totalBlocks); err != nil {
				return errors.Join(ErrCouldNotMigrate, err)
			}

			if err := mig.WaitForCompletion(); err != nil {
				return errors.Join(ErrCouldNotWaitForMigrationCompletion, err)
			}

			if err := to.SendEvent(&packets.Event{
				Type: packets.EventCompleted,
			}); err != nil {
				return errors.Join(ErrCouldNotSendEvent, err)
			}

			if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
				hook(uint32(index))
			}

			return nil
		},
	)

	if err != nil {
		panic(errors.Join(ErrCouldNotContinueWithMigration, err))
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
