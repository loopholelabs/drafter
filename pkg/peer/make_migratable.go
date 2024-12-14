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

func (resumedPeer *ResumedPeer[L, R, G]) MakeMigratable(ctx context.Context, devices []mounter.MakeMigratableDevice) (*MigratablePeer[L, R, G], error) {

	migratablePeer := &MigratablePeer[L, R, G]{
		Dg: resumedPeer.Dg,

		Close:         func() {},
		resumedPeer:   resumedPeer,
		stage4Inputs:  []makeMigratableDeviceStage{},
		resumedRunner: resumedPeer.resumedRunner,
	}

	for _, input := range resumedPeer.stage2Inputs {
		for _, device := range devices {
			if device.Name == input.name {
				migratablePeer.stage4Inputs = append(migratablePeer.stage4Inputs, makeMigratableDeviceStage{
					prev: makeMigratableFilterStage{
						prev:                 input,
						makeMigratableDevice: device,
					},
					storage: input.storage,
				})
				break
			}
		}
	}

	return migratablePeer, nil
}
