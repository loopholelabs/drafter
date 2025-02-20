package firecracker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/lithammer/shortuuid/v4"
	"github.com/loopholelabs/drafter/internal/firecracker"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/logging/types"
)

var (
	ErrCouldNotWaitForFirecracker     = errors.New("could not wait for firecracker")
	ErrCouldNotCloseServer            = errors.New("could not close server")
	ErrCouldNotRemoveVMDir            = errors.New("could not remove VM directory")
	ErrCouldNotResumeSnapshot         = errors.New("could not resume snapshot")
	ErrCouldNotCreateRecoverySnapshot = errors.New("could not create recovery snapshot")
)

type SnapshotLoadConfiguration struct {
	ExperimentalMapPrivate bool

	ExperimentalMapPrivateStateOutput  string
	ExperimentalMapPrivateMemoryOutput string
}

type Runner[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	log                     types.Logger
	VMPath                  string
	VMPid                   int
	Wait                    func() error
	Close                   func() error
	ongoingResumeWg         sync.WaitGroup
	firecrackerClient       *http.Client
	hypervisorConfiguration HypervisorConfiguration
	stateName               string
	memoryName              string
	server                  *firecracker.FirecrackerServer
	rescueCtx               context.Context
}

func StartRunner[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	log types.Logger,
	hypervisorCtx context.Context,
	rescueCtx context.Context,
	hypervisorConfiguration HypervisorConfiguration,
	stateName string,
	memoryName string,
) (
	runner *Runner[L, R, G],
	errs error,
) {
	runner = &Runner[L, R, G]{
		log:                     log,
		Wait:                    func() error { return nil },
		Close:                   func() error { return nil },
		hypervisorConfiguration: hypervisorConfiguration,
		stateName:               stateName,
		memoryName:              memoryName,
		rescueCtx:               rescueCtx,
	}

	goroutineManager := manager.NewGoroutineManager(
		hypervisorCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(errors.Join(ErrCouldNotCreateChrootBaseDirectory, err))
	}

	firecrackerCtx, cancelFirecrackerCtx := context.WithCancel(rescueCtx) // We use `rescueContext` here since this simply intercepts `hypervisorCtx`
	// and then waits for `rescueCtx` or the rescue operation to complete
	go func() {
		<-hypervisorCtx.Done() // We use hypervisorCtx, not goroutineManager.goroutineManager.Context() here since this resource outlives the function call

		runner.ongoingResumeWg.Wait()

		cancelFirecrackerCtx()
	}()

	var err error
	runner.server, err = firecracker.StartFirecrackerServer(
		firecrackerCtx, // We use firecrackerCtx (which depends on hypervisorCtx, not goroutineManager.goroutineManager.Context()) here since this resource outlives the function call
		hypervisorConfiguration.FirecrackerBin,
		hypervisorConfiguration.JailerBin,
		hypervisorConfiguration.ChrootBaseDir,
		hypervisorConfiguration.UID,
		hypervisorConfiguration.GID,
		hypervisorConfiguration.NetNS,
		hypervisorConfiguration.NumaNode,
		hypervisorConfiguration.CgroupVersion,
		hypervisorConfiguration.EnableOutput,
		hypervisorConfiguration.EnableInput,
	)
	if err != nil {
		panic(errors.Join(ErrCouldNotStartFirecrackerServer, err))
	}

	runner.VMPath = runner.server.VMPath
	runner.VMPid = runner.server.VMPid

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := runner.server.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForFirecracker, err))
		}
	})

	runner.Wait = runner.server.Wait
	runner.Close = func() error {
		if err := runner.server.Close(); err != nil {
			return errors.Join(ErrCouldNotCloseServer, err)
		}

		if err := runner.Wait(); err != nil {
			return errors.Join(ErrCouldNotWaitForFirecracker, err)
		}

		if err := os.RemoveAll(filepath.Dir(runner.VMPath)); err != nil {
			return errors.Join(ErrCouldNotRemoveVMDir, err)
		}

		return nil
	}

	runner.firecrackerClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runner.VMPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	return
}

func (runner *Runner[L, R, G]) Resume(
	ctx context.Context,
	resumeTimeout time.Duration,
	rescueTimeout time.Duration,
	agentVSockPort uint32,
	agentServerLocal L,
	agentServerHooks ipc.AgentServerAcceptHooks[R, G],
	snapshotLoadConfiguration SnapshotLoadConfiguration,
) (
	resumedRunner *ResumedRunner[L, R, G],
	errs error,
) {
	resumedRunner = &ResumedRunner[L, R, G]{
		Wait:  func() error { return nil },
		Close: func() error { return nil },

		snapshotLoadConfiguration: snapshotLoadConfiguration,

		runner: runner,
	}

	runner.ongoingResumeWg.Add(1)
	defer runner.ongoingResumeWg.Done()

	var (
		suspendOnPanicWithError = false
	)

	resumedRunner.createSnapshot = func(ctx context.Context) error {
		var (
			stateCopyName  = shortuuid.New()
			memoryCopyName = shortuuid.New()
		)
		if snapshotLoadConfiguration.ExperimentalMapPrivate {
			if err := firecracker.CreateSnapshot(
				ctx,

				runner.firecrackerClient,

				// We need to write the state and memory to a separate file since we can't truncate an `mmap`ed file
				stateCopyName,
				memoryCopyName,

				firecracker.SnapshotTypeFull,
			); err != nil {
				return errors.Join(ErrCouldNotCreateSnapshot, err)
			}
		} else {
			if err := firecracker.CreateSnapshot(
				ctx,

				runner.firecrackerClient,

				runner.stateName,
				"",

				firecracker.SnapshotTypeMsyncAndState,
			); err != nil {
				return errors.Join(ErrCouldNotCreateSnapshot, err)
			}
		}

		if snapshotLoadConfiguration.ExperimentalMapPrivate {
			if err := runner.server.Close(); err != nil {
				return errors.Join(ErrCouldNotCloseServer, err)
			}

			if err := runner.Wait(); err != nil {
				return errors.Join(ErrCouldNotWaitForFirecracker, err)
			}

			for _, device := range [][3]string{
				{runner.stateName, stateCopyName, snapshotLoadConfiguration.ExperimentalMapPrivateStateOutput},
				{runner.memoryName, memoryCopyName, snapshotLoadConfiguration.ExperimentalMapPrivateMemoryOutput},
			} {
				inputFile, err := os.Open(filepath.Join(runner.server.VMPath, device[1]))
				if err != nil {
					return errors.Join(ErrCouldNotOpenInputFile, err)
				}
				defer inputFile.Close()

				var (
					outputPath = device[2]
					addPadding = true
				)
				if outputPath == "" {
					outputPath = filepath.Join(runner.server.VMPath, device[0])
					addPadding = false
				}

				if err := os.MkdirAll(filepath.Dir(outputPath), os.ModePerm); err != nil {
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
		}

		return nil
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{
			OnAfterRecover: func() {
				if suspendOnPanicWithError {
					suspendCtx, cancelSuspendCtx := context.WithTimeout(runner.rescueCtx, rescueTimeout)
					defer cancelSuspendCtx()

					// Connections need to be closed before creating the snapshot
					if resumedRunner.acceptingAgent != nil {
						if e := resumedRunner.acceptingAgent.Close(); e != nil {
							errs = errors.Join(errs, ErrCouldNotCloseAcceptingAgent, e)
						}
					}
					if resumedRunner.agent != nil {
						resumedRunner.agent.Close()
					}

					// If a resume failed, flush the snapshot so that we can re-try
					if err := resumedRunner.createSnapshot(suspendCtx); err != nil {
						errs = errors.Join(errs, ErrCouldNotCreateRecoverySnapshot, err)
					}
				}
			},
		},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := runner.server.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForFirecracker, err))
		}
	})

	var err error
	resumedRunner.agent, err = ipc.StartAgentServer[L, R](
		filepath.Join(runner.server.VMPath, VSockName),
		uint32(agentVSockPort),

		agentServerLocal,
	)
	if err != nil {
		panic(errors.Join(ErrCouldNotStartAgentServer, err))
	}

	resumedRunner.Close = func() error {
		resumedRunner.agent.Close()

		return nil
	}

	if err := os.Chown(resumedRunner.agent.VSockPath, runner.hypervisorConfiguration.UID, runner.hypervisorConfiguration.GID); err != nil {
		panic(errors.Join(ErrCouldNotChownVSockPath, err))
	}

	{
		resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(goroutineManager.Context(), resumeTimeout)
		defer cancelResumeSnapshotAndAcceptCtx()

		if err := firecracker.ResumeSnapshot(
			resumeSnapshotAndAcceptCtx,

			runner.firecrackerClient,

			runner.stateName,
			runner.memoryName,

			!snapshotLoadConfiguration.ExperimentalMapPrivate,
		); err != nil {
			panic(errors.Join(ErrCouldNotResumeSnapshot, err))
		}

		suspendOnPanicWithError = true

		resumedRunner.acceptingAgent, err = resumedRunner.agent.Accept(
			resumeSnapshotAndAcceptCtx,
			ctx,

			agentServerHooks,
		)
		if err != nil {
			panic(errors.Join(ErrCouldNotAcceptAgent, err))
		}
		resumedRunner.Remote = &resumedRunner.acceptingAgent.Remote
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := resumedRunner.acceptingAgent.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForAcceptingAgent, err))
		}
	})

	resumedRunner.Wait = resumedRunner.acceptingAgent.Wait
	resumedRunner.Close = func() error {
		if err := resumedRunner.acceptingAgent.Close(); err != nil {
			return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
		}

		resumedRunner.agent.Close()

		if err := resumedRunner.Wait(); err != nil {
			return errors.Join(ErrCouldNotWaitForAcceptingAgent, err)
		}

		return nil
	}

	{
		afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(goroutineManager.Context(), resumeTimeout)
		defer cancelAfterResumeCtx()

		// This is a safe type cast because R is constrained by ipc.AgentServerRemote, so this specific AfterResume field
		// must be defined or there will be a compile-time error.
		// The Go Generics system can't catch this here however, it can only catch it once the type is concrete, so we need to manually cast.
		remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&resumedRunner.acceptingAgent.Remote))
		if err := remote.AfterResume(afterResumeCtx); err != nil {
			panic(errors.Join(ErrCouldNotCallAfterResumeRPC, err))
		}
	}

	return
}

type ResumedRunner[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Wait                      func() error
	Close                     func() error
	snapshotLoadConfiguration SnapshotLoadConfiguration
	createSnapshot            func(ctx context.Context) error

	runner         *Runner[L, R, G]
	agent          *ipc.AgentServer[L, R, G]
	acceptingAgent *ipc.AcceptingAgentServer[L, R, G]
	Remote         *R
}

func (resumedRunner *ResumedRunner[L, R, G]) Msync(ctx context.Context) error {
	if !resumedRunner.snapshotLoadConfiguration.ExperimentalMapPrivate {
		err := firecracker.CreateSnapshot(
			ctx,
			resumedRunner.runner.firecrackerClient,
			resumedRunner.runner.stateName,
			"",
			firecracker.SnapshotTypeMsync,
		)
		if err != nil {
			return errors.Join(ErrCouldNotCreateSnapshot, err)
		}
	}
	return nil
}

func (resumedRunner *ResumedRunner[L, R, G]) SuspendAndCloseAgentServer(ctx context.Context, suspendTimeout time.Duration) error {
	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, suspendTimeout)
	defer cancelSuspendCtx()

	// This `nil` check is required since `SuspendAndCloseAgentServer` can be run even if a runner resume failed
	if resumedRunner.Remote != nil {
		// This is a safe type cast because R is constrained by ipc.AgentServerRemote, so this specific BeforeSuspend field
		// must be defined or there will be a compile-time error.
		// The Go Generics system can't catch this here however, it can only catch it once the type is concrete, so we need to manually cast.
		remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&resumedRunner.acceptingAgent.Remote))
		if err := remote.BeforeSuspend(suspendCtx); err != nil {
			return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
		}
	}

	// Connections need to be closed before creating the snapshot
	// This `nil` check is required since `SuspendAndCloseAgentServer` can be run even if a runner resume failed
	if resumedRunner.acceptingAgent != nil {
		if err := resumedRunner.acceptingAgent.Close(); err != nil {
			return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
		}
	}

	// This `nil` check is required since `SuspendAndCloseAgentServer` can be run even if a runner resume failed
	if resumedRunner.agent != nil {
		resumedRunner.agent.Close()
	}

	if err := resumedRunner.createSnapshot(suspendCtx); err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	return nil
}
