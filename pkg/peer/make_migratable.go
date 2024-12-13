package peer

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

type ResumedPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Remote R

	Wait  func() error
	Close func() error

	resumedRunner *runner.ResumedRunner[L, R, G]

	stage2Inputs []migrateFromStage
}

func (resumedPeer *ResumedPeer[L, R, G]) MakeMigratable(
	ctx context.Context,

	devices []mounter.MakeMigratableDevice,
) (migratablePeer *MigratablePeer[L, R, G], errs error) {
	migratablePeer = &MigratablePeer[L, R, G]{
		Close: func() {},

		resumedPeer:   resumedPeer,
		stage4Inputs:  []makeMigratableDeviceStage{},
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

	stage3Inputs := []makeMigratableFilterStage{}
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

		stage3Inputs = append(stage3Inputs, makeMigratableFilterStage{
			prev: input,

			makeMigratableDevice: *makeMigratableDevice,
		})
	}

	var (
		deferFuncs [][]func() error
		err        error
	)
	migratablePeer.stage4Inputs, deferFuncs, err = utils.ConcurrentMap(
		stage3Inputs,
		func(index int, input makeMigratableFilterStage, output *makeMigratableDeviceStage, addDefer func(deferFunc func() error)) error {
			output.prev = input

			device := &MigrateToStage{
				Provider:         input.prev.storage,
				BlockSize:        input.prev.blockSize,
				VolatilityExpiry: input.makeMigratableDevice.Expiry,
				Device:           input.prev.device,
			}
			SiloMakeMigratable(device, addDefer)
			output.dirtyRemote = device.DirtyRemote
			output.storage = device.Storage
			output.totalBlocks = device.TotalBlocks
			output.orderer = device.Orderer

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
