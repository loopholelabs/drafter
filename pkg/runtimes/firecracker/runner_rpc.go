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
	log            types.Logger
	agent          *ipc.AgentServer[L, R, G]
	acceptingAgent *ipc.AgentConnection[L, R, G]
	Remote         *R
}

func (rrpc *RunnerRPC[L, R, G]) Start(vmPath string, vsockPort uint32, agentServerLocal L, uid int, gid int) error {
	var err error
	rrpc.agent, err = ipc.StartAgentServer[L, R](
		rrpc.log,
		path.Join(vmPath, VSockName),
		vsockPort,
		agentServerLocal,
	)
	if err != nil {
		return errors.Join(ErrCouldNotStartAgentServer, err)
	}

	err = os.Chown(rrpc.agent.VSockPath, uid, gid)
	if err != nil {
		return errors.Join(ErrCouldNotChownVSockPath, err)
	}
	return nil
}

func (rrpc *RunnerRPC[L, R, G]) Accept(acceptCtx context.Context, remoteCtx context.Context, agentServerHooks ipc.AgentServerAcceptHooks[R, G], errChan chan error) error {
	var err error
	rrpc.acceptingAgent, err = rrpc.agent.Accept(
		acceptCtx,
		remoteCtx,
		agentServerHooks,
	)
	if err != nil {
		return errors.Join(ErrCouldNotAcceptAgent, err)
	}
	rrpc.Remote = &rrpc.acceptingAgent.Remote

	go func() {
		err := rrpc.acceptingAgent.Wait()
		if err != nil {
			errChan <- err
		}
	}()

	return nil
}

func (rrpc *RunnerRPC[L, R, G]) Close() error {
	if rrpc.log != nil {
		rrpc.log.Debug().Msg("runnerRPC close")
	}

	if rrpc.acceptingAgent != nil {
		err := rrpc.acceptingAgent.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
		}
	}

	if rrpc.agent != nil {
		rrpc.agent.Close()
	}
	return nil
}

func (rrpc *RunnerRPC[L, R, G]) Wait() error {
	if rrpc.log != nil {
		rrpc.log.Debug().Msg("runnerRPC wait")
	}

	if rrpc.acceptingAgent != nil {
		return rrpc.acceptingAgent.Wait()
	}
	return nil
}

func (rrpc *RunnerRPC[L, R, G]) BeforeSuspend(ctx context.Context) error {
	if rrpc.log != nil {
		rrpc.log.Debug().Msg("runnerRPC beforeSuspend")
	}

	if rrpc.Remote != nil {
		// This is a safe type cast because R is constrained by ipc.AgentServerRemote, so this specific BeforeSuspend field
		// must be defined or there will be a compile-time error.
		// The Go Generics system can't catch this here however, it can only catch it once the type is concrete, so we need to manually cast.
		remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&rrpc.acceptingAgent.Remote))
		err := remote.BeforeSuspend(ctx)
		if err != nil {
			return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
		}
	}

	return nil
}

func (rrpc *RunnerRPC[L, R, G]) AfterResume(ctx context.Context, resumeTimeout time.Duration) error {
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, resumeTimeout)
	defer cancelAfterResumeCtx()

	if rrpc.log != nil {
		rrpc.log.Info().Int64("timeout_ms", resumeTimeout.Milliseconds()).Msg("runnerRPC afterResume")
	}

	// This is a safe type cast because R is constrained by ipc.AgentServerRemote, so this specific AfterResume field
	// must be defined or there will be a compile-time error.
	// The Go Generics system can't catch this here however, it can only catch it once the type is concrete, so we need to manually cast.
	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&rrpc.acceptingAgent.Remote))
	err := remote.AfterResume(afterResumeCtx)
	if err != nil {
		return errors.Join(ErrCouldNotCallAfterResumeRPC, err)
	}
	return nil
}
