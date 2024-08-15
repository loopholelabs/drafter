package peer

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type ResumedPeer struct {
	Wait  func() error
	Close func() error

	resumedRunner *runner.ResumedRunner

	stage2Inputs []stage2
	stage4Inputs []stage4
}

func (resumedPeer *ResumedPeer) MakeMigratable(
	ctx context.Context,

	devices []mounter.MakeMigratableDevice,
) (migratablePeer *MigratablePeer, errs error) {
	migratablePeer = &MigratablePeer{
		Close: func() {},

		resumedPeer:   resumedPeer,
		stage4Inputs:  resumedPeer.stage4Inputs,
		resumedRunner: resumedPeer.resumedRunner,
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	stage3Inputs := []stage3{}
	for _, input := range resumedPeer.stage2Inputs {
		var makeMigratableDevice *mounter.MakeMigratableDevice
		for _, device := range devices {
			if device.Name == input.name {
				makeMigratableDevice = &device

				break
			}
		}

		// We don't want to make this device migratable
		if makeMigratableDevice == nil {
			continue
		}

		stage3Inputs = append(stage3Inputs, stage3{
			prev: input,

			makeMigratableDevice: *makeMigratableDevice,
		})
	}

	var (
		deferFuncs [][]func() error
		err        error
	)
	resumedPeer.stage4Inputs, deferFuncs, err = utils.ConcurrentMap(
		stage3Inputs,
		func(index int, input stage3, output *stage4, addDefer func(deferFunc func() error)) error {
			output.prev = input

			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(input.prev.storage, int(input.prev.blockSize))
			output.dirtyRemote = dirtyRemote
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.prev.blockSize), input.makeMigratableDevice.Expiry)

			local := modules.NewLockable(monitor)
			output.storage = local
			addDefer(func() error {
				local.Unlock()

				return nil
			})

			input.prev.device.SetProvider(local)

			totalBlocks := (int(local.Size()) + int(input.prev.blockSize) - 1) / int(input.prev.blockSize)
			output.totalBlocks = totalBlocks

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			output.orderer = orderer
			orderer.AddAll()

			return nil
		},
	)

	migratablePeer.Close = func() {
		// Make sure that we schedule the `deferFuncs` even if we get an error
		for _, deferFuncs := range deferFuncs {
			for _, deferFunc := range deferFuncs {
				defer deferFunc() // We can safely ignore errors here since we never call `addDefer` with a function that could return an error
			}
		}
	}

	if err != nil {
		// Make sure that we schedule the `deferFuncs` even if we get an error
		migratablePeer.Close()

		panic(errors.Join(ErrCouldNotCreateMigratablePeer, err))
	}

	return
}
