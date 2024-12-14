package peer

import (
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage"
)

type migrateFromStage struct {
	name      string
	blockSize uint32
	id        uint32
	remote    bool
	storage   storage.Provider
	device    storage.ExposedStorage
}

type makeMigratableFilterStage struct {
	prev                 migrateFromStage
	makeMigratableDevice mounter.MakeMigratableDevice
}

type makeMigratableDeviceStage struct {
	prev    makeMigratableFilterStage
	storage storage.Provider
}

type migrateToStage struct {
	prev            makeMigratableDeviceStage
	migrateToDevice mounter.MigrateToDevice
}
