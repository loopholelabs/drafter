package firecracker

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/lithammer/shortuuid/v4"
	"github.com/loopholelabs/drafter/internal/firecracker"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/logging/types"
)

var (
	ErrCouldNotResumeSnapshot         = errors.New("could not resume snapshot")
	ErrCouldNotCreateRecoverySnapshot = errors.New("could not create recovery snapshot")
)

type SnapshotLoadConfiguration struct {
	ExperimentalMapPrivate             bool
	ExperimentalMapPrivateStateOutput  string
	ExperimentalMapPrivateMemoryOutput string
}

type ResumedRunner[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	log                       types.Logger
	snapshotLoadConfiguration SnapshotLoadConfiguration
	runner                    *Runner

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

	if !rr.snapshotLoadConfiguration.ExperimentalMapPrivate {
		err := firecracker.CreateSnapshotSDK(
			ctx,
			path.Join(rr.runner.VMPath, firecracker.FirecrackerSocketName),
			rr.stateName,
			"",
			firecracker.SDKSnapshotTypeMsync,
		)
		if err != nil {
			if rr.log != nil {
				rr.log.Error().Err(err).Msg("error in resumed runner Msync")
			}
			return errors.Join(ErrCouldNotCreateSnapshot, err)
		}
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

	err = rr.createSnapshot(suspendCtx)
	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	return nil
}

func (rr *ResumedRunner[L, R, G]) createSnapshot(ctx context.Context) error {
	if rr.log != nil {
		rr.log.Debug().Msg("resumedRunner createSnapshot")
	}

	stateCopyName := shortuuid.New()
	memoryCopyName := shortuuid.New()

	if rr.snapshotLoadConfiguration.ExperimentalMapPrivate {
		err := firecracker.CreateSnapshotSDK(
			ctx,
			path.Join(rr.runner.VMPath, firecracker.FirecrackerSocketName),
			// We need to write the state and memory to a separate file since we can't truncate an `mmap`ed file
			stateCopyName,
			memoryCopyName,
			firecracker.SDKSnapshotTypeFull,
		)
		if err != nil {
			return errors.Join(ErrCouldNotCreateSnapshot, err)
		}

		err = rr.runner.server.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseServer, err)
		}

		err = rr.runner.Wait()
		if err != nil {
			return errors.Join(ErrCouldNotWaitForFirecracker, err)
		}

		for _, device := range [][3]string{
			{rr.stateName, stateCopyName, rr.snapshotLoadConfiguration.ExperimentalMapPrivateStateOutput},
			{rr.memoryName, memoryCopyName, rr.snapshotLoadConfiguration.ExperimentalMapPrivateMemoryOutput},
		} {
			inputFile, err := os.Open(filepath.Join(rr.runner.server.VMPath, device[1]))
			if err != nil {
				return errors.Join(ErrCouldNotOpenInputFile, err)
			}
			defer inputFile.Close()

			outputPath := device[2]
			addPadding := true

			if outputPath == "" {
				outputPath = filepath.Join(rr.runner.server.VMPath, device[0])
				addPadding = false
			}

			err = os.MkdirAll(filepath.Dir(outputPath), os.ModePerm)
			if err != nil {
				panic(errors.Join(common.ErrCouldNotCreateOutputDir, err))
			}

			outputFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				return errors.Join(common.ErrCouldNotOpenOutputFile, err)
			}
			defer outputFile.Close()

			deviceSize, err := io.Copy(outputFile, inputFile)
			if err != nil {
				return errors.Join(ErrCouldNotCopyFile, err)
			}

			// We need to add a padding like the snapshotter if we're writing to a file instead of a block device
			if addPadding {
				if paddingLength := utils.GetBlockDevicePadding(deviceSize); paddingLength > 0 {
					if _, err := outputFile.Write(make([]byte, paddingLength)); err != nil {
						return errors.Join(ErrCouldNotWritePadding, err)
					}
				}
			}
		}

	} else {
		err := firecracker.CreateSnapshotSDK(
			ctx,
			path.Join(rr.runner.VMPath, firecracker.FirecrackerSocketName),
			rr.stateName,
			"",
			firecracker.SDKSnapshotTypeMsyncAndState,
		)
		if err != nil {
			return errors.Join(ErrCouldNotCreateSnapshot, err)
		}
	}

	return nil
}

func Resume[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	runner *Runner,
	ctx context.Context,
	resumeTimeout time.Duration,
	rescueTimeout time.Duration,
	agentVSockPort uint32,
	agentServerLocal L,
	agentServerHooks ipc.AgentServerAcceptHooks[R, G],
	snapshotLoadConfiguration SnapshotLoadConfiguration,
) (*ResumedRunner[L, R, G], error) {
	if runner.log != nil {
		runner.log.Debug().Msg("Resume resumedRunner")
	}

	resumedRunner := &ResumedRunner[L, R, G]{
		log: runner.log,
		rpc: &RunnerRPC[L, R, G]{
			log: runner.log,
		},
		snapshotLoadConfiguration: snapshotLoadConfiguration,
		runner:                    runner,
		stateName:                 common.DeviceStateName,
		memoryName:                common.DeviceMemoryName,
	}

	runner.ongoingResumeWg.Add(1)
	defer runner.ongoingResumeWg.Done()

	// Monitor for any error from the runner
	runnerErr := make(chan error, 1)
	go func() {
		err := runner.Wait()
		if err != nil {
			runnerErr <- err
		}
	}()

	err := resumedRunner.rpc.Start(runner.server.VMPath, uint32(agentVSockPort), agentServerLocal, runner.hypervisorConfiguration.UID, runner.hypervisorConfiguration.GID)
	if err != nil {
		if runner.log != nil {
			runner.log.Error().Err(err).Msg("Resume resumedRunner failed to start rpc")
		}
		return nil, err
	}

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, resumeTimeout)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = firecracker.ResumeSnapshotSDK(
		resumeSnapshotAndAcceptCtx,
		path.Join(runner.VMPath, firecracker.FirecrackerSocketName),
		resumedRunner.stateName,
		resumedRunner.memoryName,
	)

	if err != nil {
		if runner.log != nil {
			runner.log.Error().Err(err).Msg("Resume resumedRunner failed to resume snapshot")
		}
		return nil, errors.Join(ErrCouldNotResumeSnapshot, err)
	}

	// Set something up to do a snapshot if anything fails, so that we can retry etc
	recoverSnapshot := func() {
		suspendCtx, cancelSuspendCtx := context.WithTimeout(runner.rescueCtx, rescueTimeout)
		defer cancelSuspendCtx()

		err := resumedRunner.rpc.Close()
		if err != nil {
			if runner.log != nil {
				runner.log.Error().Err(err).Msg("Resume resumedRunner failed to recover")
			}
		}

		// If a resume failed, flush the snapshot so that we can re-try
		if err := resumedRunner.createSnapshot(suspendCtx); err != nil {
			if runner.log != nil {
				runner.log.Error().Err(err).Msg("Resume resumedRunner failed to recover snapshot")
			}
		}
	}

	rpcErr := make(chan error, 1)
	numRetries := 3
	for {
		// Accept RPC connection
		rpcErr = make(chan error, 1)
		err = resumedRunner.rpc.Accept(resumeSnapshotAndAcceptCtx, ctx, agentServerHooks, rpcErr)
		if err == nil {
			// Call after resume RPC
			err = resumedRunner.rpc.AfterResume(ctx, resumeTimeout)
			if err == nil {
				break
			}
		}
		runner.log.Info().Msg("Resume resumedRunner failed to call AfterResume. Retrying...")
		if numRetries == 0 {
			recoverSnapshot()
			return nil, err
		}
		numRetries--
	}

	// Check if there was any error from the runner, or from the rpc and if so return it.
	select {
	case err := <-runnerErr:
		recoverSnapshot()
		return nil, err
	case err := <-rpcErr:
		recoverSnapshot()
		return nil, err
	default:
	}

	return resumedRunner, nil
}
