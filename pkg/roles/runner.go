package roles

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/firecracker"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

const (
	VSockName = "drafter.drftsock"
)

type Runner struct {
	VMPath string

	Wait  func() error
	Close func() error

	Resume func(
		ctx context.Context,

		resumeTimeout time.Duration,
		agentVSockPort uint32,
	) (
		resumedRunner *ResumedRunner,

		errs error,
	)
}

type ResumedRunner struct {
	Wait  func() error
	Close func() error

	Msync                      func(ctx context.Context) error
	SuspendAndCloseAgentServer func(ctx context.Context, resumeTimeout time.Duration) error
}

func StartRunner(
	hypervisorCtx context.Context,
	rescueCtx context.Context,
	hypervisorConfiguration config.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	runner *Runner,

	errs error,
) {
	runner = &Runner{}

	var errsLock sync.Mutex

	internalCtx, cancel := context.WithCancelCause(hypervisorCtx)
	defer cancel(errFinished)

	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), errFinished)) {
					errs = errors.Join(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(err)
	}

	var ongoingResumeWg sync.WaitGroup

	firecrackerCtx, cancelFirecrackerCtx := context.WithCancel(rescueCtx) // We use `rescueContext` here since this simply intercepts `hypervisorCtx`
	// and then waits for `rescueCtx` or the rescue operation to complete
	go func() {
		<-hypervisorCtx.Done() // We use hypervisorCtx, not internalCtx here since this resource outlives the function call

		ongoingResumeWg.Wait()

		cancelFirecrackerCtx()
	}()

	server, err := firecracker.StartFirecrackerServer(
		firecrackerCtx, // We use firecrackerCtx (which depends on hypervisorCtx, not internalCtx) here since this resource outlives the function call

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
		panic(err)
	}

	runner.VMPath = server.VMPath

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	go func() {
		defer handleGoroutinePanic()()

		if err := server.Wait(); err != nil {
			panic(err)
		}
	}()

	runner.Wait = server.Wait
	runner.Close = func() error {
		if err := server.Close(); err != nil {
			return err
		}

		if err := runner.Wait(); err != nil {
			return err
		}

		_ = os.RemoveAll(filepath.Dir(runner.VMPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

		return nil
	}

	firecrackerClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(runner.VMPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	runner.Resume = func(
		ctx context.Context,

		resumeTimeout time.Duration,
		agentVSockPort uint32,
	) (
		resumedRunner *ResumedRunner,

		errs error,
	) {
		resumedRunner = &ResumedRunner{}

		ongoingResumeWg.Add(1)
		defer ongoingResumeWg.Done()

		var errsLock sync.Mutex

		var wg sync.WaitGroup
		defer wg.Wait()

		internalCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(errFinished)

		var (
			agent          *vsock.AgentServer
			acceptingAgent *vsock.AcceptingAgentServer
		)

		suspendOnPanicWithError := false
		handleGoroutinePanic := func() func() {
			return func() {
				if err := recover(); err != nil {
					errsLock.Lock()
					defer errsLock.Unlock()

					var e error
					if v, ok := err.(error); ok {
						e = v
					} else {
						e = fmt.Errorf("%v", err)
					}

					if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), errFinished)) {
						errs = errors.Join(errs, e)

						if suspendOnPanicWithError {
							// Connections need to be closed before creating the snapshot
							if acceptingAgent != nil && acceptingAgent.Close != nil {
								if e := acceptingAgent.Close(); e != nil {
									errs = errors.Join(errs, e)
								}
							}
							if agent != nil && agent.Close != nil {
								agent.Close()
							}

							// If a resume failed, flush the snapshot so that we can re-try
							if e := firecracker.CreateSnapshot(
								rescueCtx, // We use a separate context here so that we can
								// cancel the snapshot create action independently of the resume
								// ctx, which is typically cancelled already

								firecrackerClient,

								stateName,
								"",

								firecracker.SnapshotTypeMsyncAndState,
							); e != nil {
								errs = errors.Join(errs, e)
							}
						}
					}

					cancel(errFinished)
				}
			}
		}

		defer handleGoroutinePanic()()

		// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
		// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
		go func() {
			defer handleGoroutinePanic()()

			if err := server.Wait(); err != nil {
				panic(err)
			}
		}()

		agent, err = vsock.StartAgentServer(
			filepath.Join(server.VMPath, VSockName),
			uint32(agentVSockPort),
		)
		if err != nil {
			panic(err)
		}

		resumedRunner.Close = func() error {
			agent.Close()

			return nil
		}

		if err := os.Chown(agent.VSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
			panic(err)
		}

		if err := firecracker.ResumeSnapshot(
			internalCtx,

			firecrackerClient,

			stateName,
			memoryName,
		); err != nil {
			panic(err)
		}

		suspendOnPanicWithError = true

		{
			acceptCtx, cancel := context.WithTimeout(ctx, resumeTimeout)
			defer cancel()

			acceptingAgent, err = agent.Accept(acceptCtx, ctx)
			if err != nil {
				panic(err)
			}
		}

		// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
		// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
		go func() {
			defer handleGoroutinePanic()()

			if err := acceptingAgent.Wait(); err != nil {
				panic(err)
			}
		}()

		resumedRunner.Wait = acceptingAgent.Wait
		resumedRunner.Close = func() error {
			if err := acceptingAgent.Close(); err != nil {
				return err
			}

			agent.Close()

			return resumedRunner.Wait()
		}

		ctx, cancelResumeCtx := context.WithTimeout(internalCtx, resumeTimeout)
		defer cancelResumeCtx()

		if err := acceptingAgent.Remote.AfterResume(ctx); err != nil {
			panic(err)
		}

		resumedRunner.Msync = func(ctx context.Context) error {
			return firecracker.CreateSnapshot(
				ctx,

				firecrackerClient,

				stateName,
				"",

				firecracker.SnapshotTypeMsync,
			)
		}

		resumedRunner.SuspendAndCloseAgentServer = func(ctx context.Context, resumeTimeout time.Duration) error {
			{
				ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
				defer cancel()

				if err := acceptingAgent.Remote.BeforeSuspend(ctx); err != nil {
					panic(err)
				}
			}

			// Connections need to be closed before creating the snapshot
			if err := acceptingAgent.Close(); err != nil {
				return err
			}
			agent.Close()

			return firecracker.CreateSnapshot(
				ctx,

				firecrackerClient,

				stateName,
				"",

				firecracker.SnapshotTypeMsyncAndState,
			)
		}

		return
	}

	return
}
