package runner

import (
	"context"
	"errors"
	"time"

	"github.com/loopholelabs/drafter/pkg/roles/snapshotter"
)

func (resumedRunner *ResumedRunner) SuspendAndCloseAgentServer(ctx context.Context, suspendTimeout time.Duration) error {
	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, suspendTimeout)
	defer cancelSuspendCtx()

	if err := resumedRunner.acceptingAgent.Remote.BeforeSuspend(suspendCtx); err != nil {
		return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
	}

	// Connections need to be closed before creating the snapshot
	if err := resumedRunner.acceptingAgent.Close(); err != nil {
		return errors.Join(snapshotter.ErrCouldNotCloseAcceptingAgent, err)
	}

	resumedRunner.agent.Close()

	if err := resumedRunner.createSnapshot(suspendCtx); err != nil {
		return errors.Join(snapshotter.ErrCouldNotCreateSnapshot, err)
	}

	return nil
}
