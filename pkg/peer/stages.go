package peer

import (
	"github.com/loopholelabs/drafter/pkg/mounter"
)

type migrateFromStage struct {
	name   string
	id     uint32
	remote bool
}

type makeMigratableDeviceStage struct {
	prev                 migrateFromStage
	makeMigratableDevice mounter.MakeMigratableDevice
}

type migrateToStage struct {
	prev            makeMigratableDeviceStage
	migrateToDevice mounter.MigrateToDevice
}
