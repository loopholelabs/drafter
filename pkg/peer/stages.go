package peer

import (
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type migrateFromStage struct {
	name string

	blockSize uint32

	id     uint32
	remote bool

	storage           storage.Provider
	device            storage.ExposedStorage
	dirtyLocal        *dirtytracker.Local
	dirtyRemote       *dirtytracker.Remote
	volatilityMonitor *volatilitymonitor.VolatilityMonitor
}

type makeMigratableFilterStage struct {
	prev migrateFromStage

	makeMigratableDevice mounter.MakeMigratableDevice
}

type makeMigratableDeviceStage struct {
	prev makeMigratableFilterStage

	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.Remote
}

type migrateToStage struct {
	prev makeMigratableDeviceStage

	migrateToDevice mounter.MigrateToDevice
}
