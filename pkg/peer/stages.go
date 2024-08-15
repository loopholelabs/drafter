package peer

import (
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
)

type migrateFromStage struct {
	name string

	blockSize uint32

	id     uint32
	remote bool

	storage storage.StorageProvider
	device  storage.ExposedStorage
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
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

type migrateToStage struct {
	prev makeMigratableDeviceStage

	migrateToDevice mounter.MigrateToDevice
}
