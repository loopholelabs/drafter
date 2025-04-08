package firecracker

import (
	"context"
	"errors"
	"time"

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

	// TODO: Switch to interface here, chase away generics
	rpc *RunnerRPC[L, R, G]
}

func (rr *ResumedRunner[L, R, G]) Close() error {
	if rr.log != nil {
		rr.log.Info().Msg("closing resumed runner")
	}
	err := rr.rpc.Close()
	if err != nil {
		if rr.log != nil {
			rr.log.Error().Err(err).Msg("error closing resumed runner (rpc.Close)")
		}
		return err
	}

	err = rr.rpc.Wait()
	if err != nil {
		if rr.log != nil {
			rr.log.Error().Err(err).Msg("error closing resumed runner (rpc.Wait)")
		}
		return errors.Join(ErrCouldNotWaitForAcceptingAgent, err)
	}
	return nil
}

func (rr *ResumedRunner[L, R, G]) Wait() error {
	if rr.log != nil {
		rr.log.Info().Msg("waiting for resumed runner")
	}
	return rr.rpc.Wait()
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

	err := rr.rpc.BeforeSuspend(suspendCtx)
	if err != nil {
		return err
	}

	err = rr.rpc.Close()
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
	agentServerHooks ipc.AgentServerAcceptHooks[R, G],
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

	rpcErr := make(chan error, 1)
	numRetries := 5
	for {
		resumedRunner.rpc = &RunnerRPC[L, R, G]{
			log: machine.log,
		}

		// Start the RPC stuff...
		err := resumedRunner.rpc.Start(machine.VMPath, uint32(agentVSockPort), agentServerLocal, machine.Conf.UID, machine.Conf.GID)
		if err != nil {
			if machine.log != nil {
				machine.log.Error().Err(err).Msg("Resume resumedRunner failed to start rpc")
			}
			return nil, err
		}

		// Accept RPC connection
		rpcErr = make(chan error, 1)
		if machine.log != nil {
			machine.log.Error().Msg("Resume resumedRunner rpc Accept")
		}
		err = resumedRunner.rpc.Accept(resumeSnapshotAndAcceptCtx, ctx, agentServerHooks, rpcErr)
		if machine.log != nil {
			machine.log.Error().Err(err).Msg("Resume resumedRunner rpc Accept returned")
		}
		if err == nil {
			// Call after resume RPC
			err = resumedRunner.rpc.AfterResume(ctx, resumeTimeout)
			if err == nil {
				break
			}
		}
		if machine.log != nil {
			machine.log.Info().Err(err).Msg("Resume resumedRunner failed to call AfterResume. Retrying...")
		}

		// Close it
		resumedRunner.rpc.Close()

		if numRetries == 0 {
			return nil, err
		}
		numRetries--
		time.Sleep(200 * time.Millisecond)
	}

	// Check if there was any error from the runner, or from the rpc and if so return it.
	select {
	case err := <-runnerErr:
		return nil, err
	case err := <-rpcErr:
		return nil, err
	default:
	}

	return resumedRunner, nil
}
