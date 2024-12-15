package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
)

type MigrateToStage struct {
	Name             string        // input.prev.prev.prev.name
	VolatilityExpiry time.Duration // input.makeMigratableDevice.Expiry
	Remote           bool          // input.prev.prev.prev.remote
	MaxDirtyBlocks   int           // input.migrateToDevice.MaxDirtyBlocks
	MinCycles        int           // input.migrateToDevice.MinCycles
	MaxCycles        int           // input.migrateToDevice.MaxCycles
	CycleThrottle    time.Duration // input.migrateToDevice.CycleThrottle
}

// This deals with VM stuff
type VMStateManager struct {
	checkSuspendedVM  func() bool
	suspendAndMsyncVM func() error
	suspendedVMCh     chan struct{}
	MSync             func(context.Context) error
}

func SiloMigrateTo(dg *devicegroup.DeviceGroup,
	devices []*MigrateToStage,
	concurrency int,
	goroutineManager *manager.GoroutineManager,
	pro protocol.Protocol,
	hooks MigrateToHooks,
	vmState *VMStateManager,
) error {

	// Start a migration to the protocol...
	err := dg.StartMigrationTo(pro)
	if err != nil {
		return err
	}

	// TODO: Get this to call hooks
	pHandler := func(p []*migrator.MigrationProgress) {
		for index, prog := range p {
			fmt.Printf("[%d] Progress (%d/%d)\n", index, prog.ReadyBlocks, prog.TotalBlocks)
		}
	}

	fmt.Printf("Starting MigrateAll...\n")

	err = dg.MigrateAll(concurrency, pHandler)
	if err != nil {
		return err
	}

	fmt.Printf("MigrateAll done...\n")

	var devicesLeftToTransferAuthorityFor atomic.Int32
	errs := make(chan error, len(devices))

	// And now do the dirty migration phase for each device...
	for forIndex, forInput := range devices {
		go func(index int, input *MigrateToStage) {
			errs <- func() error {
				info := dg.GetDeviceInformationByName(input.Name)
				return doDirtyMigration(goroutineManager, input, hooks, vmState, info.Migrator, index, info.To,
					&devicesLeftToTransferAuthorityFor, len(devices), int(info.BlockSize),
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
	blockSize int,
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
			if err := to.DirtyList(blockSize, blocks); err != nil {
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
