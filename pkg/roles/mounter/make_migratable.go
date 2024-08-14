package mounter

import (
	"context"
	"errors"
	"time"

	iutils "github.com/loopholelabs/drafter/internal/utils"
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

	stage2Inputs []PeerStage2
}

func (migratedMounter *MigratedMounter) MakeMigratable(
	ctx context.Context,

	devices []MakeMigratableDevice,
) (migratableMounter *MigratableMounter, errs error) {
	migratableMounter = &MigratableMounter{
		Close: func() {},
	}

	stage3Inputs := []PeerStage3{}
	for _, input := range migratedMounter.stage2Inputs {
		var makeMigratableDevice *MakeMigratableDevice
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

		stage3Inputs = append(stage3Inputs, PeerStage3{
			Prev: input,

			MakeMigratableDevice: *makeMigratableDevice,
		})
	}

	var (
		deferFuncs [][]func() error
		err        error
	)
	migratableMounter.stage4Inputs, deferFuncs, err = iutils.ConcurrentMap(
		stage3Inputs,
		func(index int, input PeerStage3, output *PeerStage4, addDefer func(deferFunc func() error)) error {
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
