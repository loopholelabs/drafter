package firecracker

import (
	"context"
	"errors"
	"os"
	"path"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
)

type RunnerRPC[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	log   types.Logger
	agent *ipc.AgentRPC[L, R, G]
}

func (rrpc *RunnerRPC[L, R, G]) Start(vmPath string, vsockPort uint32, agentServerLocal L, uid int, gid int) error {
	var err error
	rrpc.agent, err = ipc.StartAgentRPC[L, R](rrpc.log, path.Join(vmPath, VSockName), vsockPort, agentServerLocal)
	if err != nil {
		return errors.Join(ErrCouldNotStartAgentServer, err)
	}

	err = os.Chown(rrpc.agent.VSockPath, uid, gid)
	if err != nil {
		return errors.Join(ErrCouldNotChownVSockPath, err)
	}
	return nil
}

func (rrpc *RunnerRPC[L, R, G]) BeforeSuspend(ctx context.Context) error {
	if rrpc.log != nil {
		rrpc.log.Debug().Msg("runnerRPC beforeSuspend")
	}

	r, err := rrpc.agent.GetRemote(ctx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(ctx)
	if err != nil {
		return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
	}

	return nil
}

func (rrpc *RunnerRPC[L, R, G]) AfterResume(ctx context.Context, resumeTimeout time.Duration) error {
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, resumeTimeout)
	defer cancelAfterResumeCtx()

	if rrpc.log != nil {
		rrpc.log.Info().Int64("timeout_ms", resumeTimeout.Milliseconds()).Msg("runnerRPC AfterResume")
	}

	r, err := rrpc.agent.GetRemote(afterResumeCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		return errors.Join(ErrCouldNotCallAfterResumeRPC, err)
	}
	return nil
}
