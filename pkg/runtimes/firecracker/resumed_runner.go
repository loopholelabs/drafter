package firecracker

import (
	"context"
	"errors"
	"path"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
)

var (
	ErrCouldNotResumeSnapshot = errors.New("could not resume snapshot")
)

type ResumedRunner[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	log     types.Logger
	machine *FirecrackerMachine

	stateName  string
	memoryName string

	agent *ipc.AgentRPC[L, R, G]
}

func (rr *ResumedRunner[L, R, G]) Close() error {
	if rr.log != nil {
		rr.log.Info().Msg("closing resumed runner")
	}

	if rr.agent != nil {
		err := rr.agent.Close()
		if err != nil {
			if rr.log != nil {
				rr.log.Error().Err(err).Msg("error closing resumed runner (rpc.Close)")
			}
			return err
		}
	}

	return nil
}

func (rr *ResumedRunner[L, R, G]) Msync(ctx context.Context) error {
	if rr.log != nil {
		rr.log.Info().Msg("resumed runner Msync")
	}

	err := rr.machine.CreateSnapshot(
		ctx,
		rr.stateName,
		"",
		SDKSnapshotTypeMsync,
	)
	if err != nil {
		if rr.log != nil {
			rr.log.Error().Err(err).Msg("error in resumed runner Msync")
		}
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}
	return nil
}

func (rr *ResumedRunner[L, R, G]) SuspendAndCloseAgentServer(ctx context.Context, suspendTimeout time.Duration) error {
	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, suspendTimeout)
	defer cancelSuspendCtx()

	r, err := rr.agent.GetRemote(suspendCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	if err != nil {
		return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
	}

	err = rr.agent.Close()
	if err != nil {
		return err
	}

	if rr.log != nil {
		rr.log.Debug().Msg("resumedRunner createSnapshot")
	}

	err = rr.machine.CreateSnapshot(suspendCtx, rr.stateName, "", SDKSnapshotTypeMsyncAndState)
	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	return nil
}

func Resume[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	machine *FirecrackerMachine,
	ctx context.Context,
	resumeTimeout time.Duration,
	rescueTimeout time.Duration,
	agentVSockPort uint32,
	agentServerLocal L,
) (*ResumedRunner[L, R, G], error) {

	if machine.log != nil {
		machine.log.Debug().Msg("Resume resumedRunner")
	}

	resumedRunner := &ResumedRunner[L, R, G]{
		log:        machine.log,
		machine:    machine,
		stateName:  common.DeviceStateName,
		memoryName: common.DeviceMemoryName,
	}

	// Monitor for any error from the runner
	runnerErr := make(chan error, 1)
	go func() {
		err := machine.Wait()
		if err != nil {
			runnerErr <- err
		}
	}()

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, resumeTimeout)
	defer cancelResumeSnapshotAndAcceptCtx()

	err := machine.ResumeSnapshot(resumeSnapshotAndAcceptCtx, resumedRunner.stateName, resumedRunner.memoryName)

	if err != nil {
		if machine.log != nil {
			machine.log.Error().Err(err).Msg("Resume resumedRunner failed to resume snapshot")
		}
		return nil, errors.Join(ErrCouldNotResumeSnapshot, err)
	}

	// Start the RPC stuff...
	resumedRunner.agent, err = ipc.StartAgentRPC[L, R](
		machine.log, path.Join(machine.VMPath, VSockName),
		uint32(agentVSockPort), agentServerLocal)

	if err != nil {
		if machine.log != nil {
			machine.log.Error().Err(err).Msg("Resume resumedRunner failed to start rpc")
		}
		return nil, err
	}

	if machine.log != nil {
		machine.log.Debug().Msg("Resume resumedRunner rpc AfterResume")
	}
	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, resumeTimeout)
	defer cancelAfterResumeCtx()

	r, err := resumedRunner.agent.GetRemote(afterResumeCtx)
	if err != nil {
		return nil, err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		return nil, err
	}

	// Check if there was any error from the runner, or from the rpc and if so return it.
	select {
	case err := <-runnerErr:
		return nil, err
	default:
	}

	return resumedRunner, nil
}
