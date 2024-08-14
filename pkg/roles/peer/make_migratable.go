package peer

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/roles/mounter"
	"github.com/loopholelabs/drafter/pkg/roles/runner"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type ResumedPeer struct {
	Wait  func() error
	Close func() error

	resumedRunner *runner.ResumedRunner

	stage2Inputs []mounter.PeerStage2
	stage4Inputs []mounter.PeerStage4
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

	stage3Inputs := []mounter.PeerStage3{}
	for _, input := range resumedPeer.stage2Inputs {
		var makeMigratableDevice *mounter.MakeMigratableDevice
		for _, device := range devices {
			if device.Name == input.Name {
				makeMigratableDevice = &device

				break
			}
		}

		// We don't want to make this device migratable
		if makeMigratableDevice == nil {
			continue
		}

		stage3Inputs = append(stage3Inputs, mounter.PeerStage3{
			Prev: input,

			MakeMigratableDevice: *makeMigratableDevice,
		})
	}

	var (
		deferFuncs [][]func() error
		err        error
	)
	resumedPeer.stage4Inputs, deferFuncs, err = utils.ConcurrentMap(
		stage3Inputs,
		func(index int, input mounter.PeerStage3, output *mounter.PeerStage4, addDefer func(deferFunc func() error)) error {
			output.Prev = input

			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(input.Prev.Storage, int(input.Prev.BlockSize))
			output.DirtyRemote = dirtyRemote
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.Prev.BlockSize), input.MakeMigratableDevice.Expiry)

			local := modules.NewLockable(monitor)
			output.Storage = local
			addDefer(func() error {
				local.Unlock()

				return nil
			})

			input.Prev.Device.SetProvider(local)

			totalBlocks := (int(local.Size()) + int(input.Prev.BlockSize) - 1) / int(input.Prev.BlockSize)
			output.TotalBlocks = totalBlocks

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			output.Orderer = orderer
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
