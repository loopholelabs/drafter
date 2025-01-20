package peer

import (
	"context"
	"io"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

type ResumedPeer struct {
	dg            *devicegroup.DeviceGroup
	Remote        ipc.AgentServerRemote[struct{}]
	resumedRunner *runner.ResumedRunner[struct{}, ipc.AgentServerRemote[struct{}], struct{}]

	log types.Logger

	alreadyClosed bool
	alreadyWaited bool
}

func (resumedPeer *ResumedPeer) Wait() error {
	if resumedPeer.log != nil {
		resumedPeer.log.Debug().Msg("resumedPeer.Wait")
	}
	if resumedPeer.alreadyWaited {
		if resumedPeer.log != nil {
			resumedPeer.log.Trace().Msg("FIXME: ResumedPeer.Wait called multiple times")
		}
		return nil
	}
	resumedPeer.alreadyWaited = true

	if resumedPeer.resumedRunner != nil {
		return resumedPeer.resumedRunner.Wait()
	}
	return nil
}

func (resumedPeer *ResumedPeer) Close() error {
	if resumedPeer.log != nil {
		resumedPeer.log.Debug().Msg("resumedPeer.Close")
	}
	if resumedPeer.alreadyClosed {
		if resumedPeer.log != nil {
			resumedPeer.log.Trace().Msg("FIXME: ResumedPeer.Close called multiple times")
		}
		return nil
	}
	resumedPeer.alreadyClosed = true

	if resumedPeer.resumedRunner != nil {
		return resumedPeer.resumedRunner.Close()
	}
	return nil
}

func (resumedPeer *ResumedPeer) SuspendAndCloseAgentServer(ctx context.Context, resumeTimeout time.Duration) error {
	if resumedPeer.log != nil {
		resumedPeer.log.Debug().Msg("resumedPeer.SuspendAndCloseAgentServer")
	}
	err := resumedPeer.resumedRunner.SuspendAndCloseAgentServer(
		ctx,
		resumeTimeout,
	)
	if err != nil {
		if resumedPeer.log != nil {
			resumedPeer.log.Warn().Err(err).Msg("error from resumedPeer.SuspendAndCloseAgentServer")
		}
	}
	return err
}

// Callbacks
type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllMigrationsCompleted func()
	OnProgress               func(p map[string]*migrator.MigrationProgress)
	GetXferCustomData        func() []byte
}

/**
 * MigrateTo migrates to a remote VM.
 *
 *
 */
func (resumedPeer *ResumedPeer) MigrateTo(ctx context.Context, devices []common.MigrateToDevice,
	suspendTimeout time.Duration, concurrency int, readers []io.Reader, writers []io.Writer,
	hooks MigrateToHooks) error {

	if resumedPeer.log != nil {
		resumedPeer.log.Info().Msg("resumedPeer.MigrateTo")
	}

	// This manages the status of the VM - if it's suspended or not.
	vmState := common.NewVMStateMgr(ctx,
		resumedPeer.SuspendAndCloseAgentServer,
		suspendTimeout,
		resumedPeer.resumedRunner.Msync,
		hooks.OnBeforeSuspend,
		hooks.OnAfterSuspend,
	)

	err := common.MigrateToPipe(ctx, readers, writers, resumedPeer.dg, concurrency, hooks.OnProgress, vmState, devices, hooks.GetXferCustomData)
	if err != nil {
		if resumedPeer.log != nil {
			resumedPeer.log.Info().Err(err).Msg("error in resumedPeer.MigrateTo")
		}
		return err
	}

	if resumedPeer.log != nil {
		resumedPeer.log.Info().Msg("resumedPeer.MigrateTo completed successfuly")
	}

	return nil
}
