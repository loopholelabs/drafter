package runner

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
)

func (resumedRunner *ResumedRunner[L, R]) SuspendAndCloseAgentServer(ctx context.Context, suspendTimeout time.Duration) error {
	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, suspendTimeout)
	defer cancelSuspendCtx()

	// This is a safe type cast because R is constrained by ipc.AgentServerRemote, so this specific BeforeSuspend field
	// must be defined or there will be a compile-time error.
	// The Go Generics system can't catch this here however, it can only catch it once the type is concrete, so we need to manually cast.
	if err := reflect.ValueOf(resumedRunner.acceptingAgent.Remote).Interface().(ipc.AgentServerRemote).BeforeSuspend(suspendCtx); err != nil {
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
