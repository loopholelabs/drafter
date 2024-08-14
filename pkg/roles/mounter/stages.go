package mounter

import (
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/modules"
)

type PeerStage2 struct {
	Name string

	BlockSize uint32

	ID     uint32
	Remote bool

	Storage storage.StorageProvider
	Device  storage.ExposedStorage
}

type PeerStage3 struct {
	Prev PeerStage2

	MakeMigratableDevice MakeMigratableDevice
}

type PeerStage4 struct {
	Prev PeerStage3

	Storage     *modules.Lockable
	Orderer     *blocks.PriorityBlockOrder
	TotalBlocks int
	DirtyRemote *dirtytracker.DirtyTrackerRemote
}

type PeerStage5 struct {
	Prev PeerStage4

	MigrateToDevice MigrateToDevice
}
