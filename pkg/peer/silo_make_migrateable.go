package peer

import (
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

func SiloMakeMigratable(input *MigrateToStage, addDefer func(func() error)) {
	dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(input.Provider, int(input.BlockSize))
	input.DirtyRemote = dirtyRemote
	monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.BlockSize), input.VolatilityExpiry)

	local := modules.NewLockable(monitor)
	input.Storage = local
	addDefer(func() error {
		local.Unlock()
		return nil
	})
	input.Device.SetProvider(local)

	totalBlocks := (int(local.Size()) + int(input.BlockSize) - 1) / int(input.BlockSize)

	orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
	orderer.AddAll()
	input.Orderer = orderer
}
