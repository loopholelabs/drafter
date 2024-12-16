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

func SiloMigrateDirtyTo(dg *devicegroup.DeviceGroup,
	devices []mounter.MigrateToDevice,
	concurrency int,
	goroutineManager *manager.GoroutineManager,
	pro protocol.Protocol,
	hooks MigrateToHooks,
	vmState *VMStateMgr,
) error {

	var devicesLeftToTransferAuthorityFor atomic.Int32
	errs := make(chan error, len(devices))

	// And now do the dirty migration phase for each device...
	for forIndex, forInput := range devices {
		go func(index int, input mounter.MigrateToDevice) {
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
	input mounter.MigrateToDevice,
	hooks MigrateToHooks,
	vmState *VMStateMgr,
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

	cyclesBelowDirtyBlockTreshold := 0
	totalCycles := 0

	for {
		// We only need to `msync` for the memory because `msync` only affects the memory
		if !vmState.CheckSuspendedVM() && input.Name == packager.MemoryName {
			err := vmState.Msync()
			if err != nil {
				return errors.Join(ErrCouldNotMsyncRunner, err)
			}
		}

		blocks := mig.GetLatestDirty()
		if blocks == nil {
			mig.Unlock()

			if vmState.CheckSuspendedVM() {
				break // Only exit when we have zero dirty blocks and the vm is suspended.
			}
		}

		if blocks != nil {
			err := to.DirtyList(blockSize, blocks)
			if err != nil {
				return errors.Join(mounter.ErrCouldNotSendDirtyList, err)
			}

			err = mig.MigrateDirty(blocks)
			if err != nil {
				panic(errors.Join(mounter.ErrCouldNotMigrateDirtyBlocks, err))
			}
		}

		if !vmState.CheckSuspendedVM() && !(devicesLeftToTransferAuthorityFor.Load() >= int32(numDevices)) {

			// We use the background context here instead of the internal context because we want to distinguish
			// between a context cancellation from the outside and getting a response
			cycleThrottleCtx, cancelCycleThrottleCtx := context.WithTimeout(context.Background(), input.CycleThrottle)
			defer cancelCycleThrottleCtx()

			select {
			case <-cycleThrottleCtx.Done():
				break

			case <-vmState.GetSuspsnededVMCh():
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
			err := vmState.SuspendAndMsync()
			fmt.Printf("%v Suspended VM\n", time.Now())

			if err != nil {
				return errors.Join(mounter.ErrCouldNotSuspendAndMsyncVM, err)
			}
		}
	}

	fmt.Printf("%v Transfer authority\n", time.Now())
	if err := to.SendEvent(&packets.Event{
		Type:       packets.EventCustom,
		CustomType: byte(registry.EventCustomTransferAuthority),
	}); err != nil {
		panic(errors.Join(mounter.ErrCouldNotSendTransferAuthorityEvent, err))
	}

	if err := mig.WaitForCompletion(); err != nil {
		return errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err)
	}

	return nil
}
