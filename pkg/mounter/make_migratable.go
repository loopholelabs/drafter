package mounter

import (
	"context"
	"errors"
	"time"

	iutils "github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type MigratedDevice struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type MakeMigratableDevice struct {
	Name string `json:"name"`

	Expiry time.Duration `json:"expiry"`
}

type MigratedMounter struct {
	Devices []MigratedDevice

	Wait  func() error
	Close func() error

	stage2Inputs []migrateFromAndMountStage
}

func (migratedMounter *MigratedMounter) MakeMigratable(
	ctx context.Context,

	devices []MakeMigratableDevice,
) (migratableMounter *MigratableMounter, errs error) {
	migratableMounter = &MigratableMounter{
		Close: func() {},
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
	for _, input := range migratedMounter.stage2Inputs {
		var makeMigratableDevice *MakeMigratableDevice
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
	migratableMounter.stage4Inputs, deferFuncs, err = iutils.ConcurrentMap(
		stage3Inputs,
		func(index int, input makeMigratableFilterStage, output *makeMigratableDeviceStage, addDefer func(deferFunc func() error)) error {
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

	migratableMounter.Close = func() {
		// Make sure that we schedule the `deferFuncs` even if we get an error
		for _, deferFuncs := range deferFuncs {
			for _, deferFunc := range deferFuncs {
				defer deferFunc() // We can safely ignore errors here since we never call `addDefer` with a function that could return an error
			}
		}
	}

	if err != nil {
		// Make sure that we schedule the `deferFuncs` even if we get an error
		migratableMounter.Close()

		panic(errors.Join(ErrCouldNotCreateMigratableMounter, err))
	}

	return
}
