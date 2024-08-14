package mounter

import (
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
)

type stage2 struct {
	name string

	blockSize uint32

	id     uint32
	remote bool

	storage storage.StorageProvider
	device  storage.ExposedStorage
}

type stage3 struct {
	prev stage2

	makeMigratableDevice MakeMigratableDevice
}

type stage4 struct {
	prev stage3

	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

type stage5 struct {
	prev stage4

	migrateToDevice MigrateToDevice
}