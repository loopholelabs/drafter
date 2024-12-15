package peer

import (
	"context"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type ResumedPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Dg            *devicegroup.DeviceGroup
	Remote        R
	Wait          func() error
	Close         func() error
	resumedRunner *runner.ResumedRunner[L, R, G]
	stage2Inputs  []migrateFromStage
}

// FIXME: This can go, it does nothing...

func (resumedPeer *ResumedPeer[L, R, G]) MakeMigratable(ctx context.Context, devices []mounter.MakeMigratableDevice) (*MigratablePeer[L, R, G], error) {

	migratablePeer := &MigratablePeer[L, R, G]{
		Dg: resumedPeer.Dg,

		Close:         func() {},
		resumedPeer:   resumedPeer,
		resumedRunner: resumedPeer.resumedRunner,
	}

	return migratablePeer, nil
}
