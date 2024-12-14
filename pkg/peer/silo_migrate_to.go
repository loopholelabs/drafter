package peer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type MigrateToStage struct {
	Size             uint64                 // input.prev.storage.Size()
	Name             string                 // input.prev.prev.prev.name
	BlockSize        uint32                 // input.prev.prev.prev.blockSize
	Storage          storage.Provider       // input.prev.storage
	Device           storage.ExposedStorage // input.prev.device
	VolatilityExpiry time.Duration          // input.makeMigratableDevice.Expiry
	Remote           bool                   // input.prev.prev.prev.remote
	MaxDirtyBlocks   int                    // input.migrateToDevice.MaxDirtyBlocks
	MinCycles        int                    // input.migrateToDevice.MinCycles
	MaxCycles        int                    // input.migrateToDevice.MaxCycles
	CycleThrottle    time.Duration          // input.migrateToDevice.CycleThrottle
}

// This deals with VM stuff
type VMStateManager struct {
	checkSuspendedVM  func() bool
	suspendAndMsyncVM func() error
	suspendedVMCh     chan struct{}
	MSync             func(context.Context) error
}

func SiloMigrateTo(devices []*MigrateToStage,
	concurrency int,
	goroutineManager *manager.GoroutineManager,
	pro protocol.Protocol,
	hooks MigrateToHooks,
	vmState *VMStateManager,
) error {
	var devicesLeftToSend atomic.Int32
	var devicesLeftToTransferAuthorityFor atomic.Int32

	errs := make(chan error, len(devices))

	migrators := make([]*migrator.Migrator, len(devices))
	tos := make([]*protocol.ToProtocol, len(devices))

	for forIndex, forInput := range devices {
		go func(index int, input *MigrateToStage) {
			errs <- func() error {
				// Right now, we are doing devices in series here...

				//
				dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(input.Storage, int(input.BlockSize))
				volatilityMonitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.BlockSize), input.VolatilityExpiry)

				local := modules.NewLockable(volatilityMonitor)
				input.Device.SetProvider(local)
				//

				totalBlocks := (int(input.Size) + int(input.BlockSize) - 1) / int(input.BlockSize)
				orderer := blocks.NewPriorityBlockOrder(totalBlocks, volatilityMonitor)
				orderer.AddAll()

				to := protocol.NewToProtocol(input.Size, uint32(index), pro)

				if err := to.SendDevInfo(input.Name, input.BlockSize, ""); err != nil {
					return errors.Join(mounter.ErrCouldNotSendDevInfo, err)
				}

				if hook := hooks.OnDeviceSent; hook != nil {
					hook(uint32(index), input.Remote)
				}

				devicesLeftToSend.Add(1)
				if devicesLeftToSend.Load() >= int32(len(devices)) {
					goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
						if err := to.SendEvent(&packets.Event{
							Type:       packets.EventCustom,
							CustomType: byte(registry.EventCustomAllDevicesSent),
						}); err != nil {
							panic(errors.Join(mounter.ErrCouldNotSendAllDevicesSentEvent, err))
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
						if endOffset > uint64(input.Size) {
							endOffset = uint64(input.Size)
						}

						startBlock := int(offset / int64(input.BlockSize))
						endBlock := int((endOffset-1)/uint64(input.BlockSize)) + 1
						for b := startBlock; b < endBlock; b++ {
							orderer.PrioritiseBlock(b)
						}
					}); err != nil {
						panic(errors.Join(registry.ErrCouldNotHandleNeedAt, err))
					}
				})

				goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
					if err := to.HandleDontNeedAt(func(offset int64, length int32) {
						// Deprioritize blocks
						endOffset := uint64(offset + int64(length))
						if endOffset > uint64(input.Size) {
							endOffset = uint64(input.Size)
						}

						startBlock := int(offset / int64(input.Size))
						endBlock := int((endOffset-1)/uint64(input.Size)) + 1
						for b := startBlock; b < endBlock; b++ {
							orderer.Remove(b)
						}
					}); err != nil {
						panic(errors.Join(registry.ErrCouldNotHandleDontNeedAt, err))
					}
				})

				cfg := migrator.NewConfig().WithBlockSize(int(input.BlockSize))
				cfg.Concurrency = map[int]int{
					storage.BlockTypeAny: concurrency,
				}
				cfg.LockerHandler = func() {
					defer goroutineManager.CreateBackgroundPanicCollector()()

					if err := to.SendEvent(&packets.Event{
						Type: packets.EventPreLock,
					}); err != nil {
						panic(errors.Join(mounter.ErrCouldNotSendPreLockEvent, err))
					}

					local.Lock()

					if err := to.SendEvent(&packets.Event{
						Type: packets.EventPostLock,
					}); err != nil {
						panic(errors.Join(mounter.ErrCouldNotSendPostLockEvent, err))
					}
				}
				cfg.UnlockerHandler = func() {
					defer goroutineManager.CreateBackgroundPanicCollector()()

					if err := to.SendEvent(&packets.Event{
						Type: packets.EventPreUnlock,
					}); err != nil {
						panic(errors.Join(mounter.ErrCouldNotSendPreUnlockEvent, err))
					}

					local.Unlock()

					if err := to.SendEvent(&packets.Event{
						Type: packets.EventPostUnlock,
					}); err != nil {
						panic(errors.Join(mounter.ErrCouldNotSendPostUnlockEvent, err))
					}
				}
				cfg.ErrorHandler = func(b *storage.BlockInfo, err error) {
					defer goroutineManager.CreateBackgroundPanicCollector()()

					if err != nil {
						panic(errors.Join(registry.ErrCouldNotContinueWithMigration, err))
					}
				}
				cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
					if hook := hooks.OnDeviceInitialMigrationProgress; hook != nil {
						hook(uint32(index), input.Remote, p.ReadyBlocks, p.TotalBlocks)
					}
				}

				mig, err := migrator.NewMigrator(dirtyRemote, to, orderer, cfg)
				if err != nil {
					return errors.Join(registry.ErrCouldNotCreateMigrator, err)
				}

				if err := mig.Migrate(totalBlocks); err != nil {
					return errors.Join(mounter.ErrCouldNotMigrateBlocks, err)
				}

				if err := mig.WaitForCompletion(); err != nil {
					return errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err)
				}

				migrators[index] = mig
				tos[index] = to

				return nil
			}()
		}(forIndex, forInput)
	}

	// Wait for all of these to complete...
	for range devices {
		err := <-errs
		if err != nil {
			return err
		}
	}

	// And now do the dirty migration phase...
	for forIndex, forInput := range devices {
		go func(index int, input *MigrateToStage) {
			errs <- func() error {
				mig := migrators[index]
				to := tos[index]
				return doDirtyMigration(goroutineManager, input, hooks, vmState, mig, index, to,
					&devicesLeftToTransferAuthorityFor, len(devices),
				)
			}()
		}(forIndex, forInput)
	}

	// Wait for all of these to complete...
	for range devices {
		err := <-errs
		if err != nil {
			return err
		}
	}

	return nil
}

// Handle dirtyMigrationBlocks separately here...
func doDirtyMigration(goroutineManager *manager.GoroutineManager,
	input *MigrateToStage,
	hooks MigrateToHooks,
	vmState *VMStateManager,
	mig *migrator.Migrator,
	index int,
	to *protocol.ToProtocol,
	devicesLeftToTransferAuthorityFor *atomic.Int32,
	numDevices int,
) error {
	markDeviceAsReadyForAuthorityTransfer := sync.OnceFunc(func() {
		devicesLeftToTransferAuthorityFor.Add(1)
	})

	var (
		cyclesBelowDirtyBlockTreshold = 0
		totalCycles                   = 0
		ongoingMigrationsWg           sync.WaitGroup
	)
	for {
		suspendedVM := vmState.checkSuspendedVM()
		// We only need to `msync` for the memory because `msync` only affects the memory
		if !suspendedVM && input.Name == packager.MemoryName {
			if err := vmState.MSync(goroutineManager.Context()); err != nil {

				return errors.Join(ErrCouldNotMsyncRunner, err)
			}
		}

		ongoingMigrationsWg.Wait()

		if hook := hooks.OnBeforeGetDirtyBlocks; hook != nil {
			hook(uint32(index), input.Remote)
		}

		blocks := mig.GetLatestDirty()
		if blocks == nil {
			mig.Unlock()

			if vmState.checkSuspendedVM() {
				break
			}
		}

		if blocks != nil {
			if err := to.DirtyList(int(input.BlockSize), blocks); err != nil {
				return errors.Join(mounter.ErrCouldNotSendDirtyList, err)
			}

			ongoingMigrationsWg.Add(1)
			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				defer ongoingMigrationsWg.Done()

				if err := mig.MigrateDirty(blocks); err != nil {
					panic(errors.Join(mounter.ErrCouldNotMigrateDirtyBlocks, err))
				}

				if vmState.checkSuspendedVM() {
					if hook := hooks.OnDeviceFinalMigrationProgress; hook != nil {
						hook(uint32(index), input.Remote, len(blocks))
					}
				} else {
					if hook := hooks.OnDeviceContinousMigrationProgress; hook != nil {
						hook(uint32(index), input.Remote, len(blocks))
					}
				}
			})
		}

		if !vmState.checkSuspendedVM() && !(devicesLeftToTransferAuthorityFor.Load() >= int32(numDevices)) {

			// We use the background context here instead of the internal context because we want to distinguish
			// between a context cancellation from the outside and getting a response
			cycleThrottleCtx, cancelCycleThrottleCtx := context.WithTimeout(context.Background(), input.CycleThrottle)
			defer cancelCycleThrottleCtx()

			select {
			case <-cycleThrottleCtx.Done():
				break

			case <-vmState.suspendedVMCh:
				break

			case <-goroutineManager.Context().Done(): // ctx is the goroutineManager.goroutineManager.Context() here
				if err := goroutineManager.Context().Err(); err != nil {
					return errors.Join(ErrPeerContextCancelled, err)
				}

				return nil
			}
		}

		totalCycles++
		if len(blocks) < input.MaxDirtyBlocks {
			cyclesBelowDirtyBlockTreshold++
			if cyclesBelowDirtyBlockTreshold > input.MinCycles {
				markDeviceAsReadyForAuthorityTransfer()
			}
		} else if totalCycles > input.MaxCycles {
			markDeviceAsReadyForAuthorityTransfer()
		} else {
			cyclesBelowDirtyBlockTreshold = 0
		}

		if devicesLeftToTransferAuthorityFor.Load() >= int32(numDevices) {
			if err := vmState.suspendAndMsyncVM(); err != nil {
				return errors.Join(mounter.ErrCouldNotSuspendAndMsyncVM, err)
			}
		}
	}

	if err := to.SendEvent(&packets.Event{
		Type:       packets.EventCustom,
		CustomType: byte(registry.EventCustomTransferAuthority),
	}); err != nil {
		panic(errors.Join(mounter.ErrCouldNotSendTransferAuthorityEvent, err))
	}

	if hook := hooks.OnDeviceAuthoritySent; hook != nil {
		hook(uint32(index), input.Remote)
	}

	if err := mig.WaitForCompletion(); err != nil {
		return errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err)
	}

	if err := to.SendEvent(&packets.Event{
		Type: packets.EventCompleted,
	}); err != nil {
		return errors.Join(mounter.ErrCouldNotSendCompletedEvent, err)
	}

	if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
		hook(uint32(index), input.Remote)
	}

	return nil
}
