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

func StartRunner(
	ctx context.Context,
	hypervisorConfiguration config.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	vmPath string,

	waitFunc func() error,
	closeFunc func() error,

	resumeFunc func(
		ctx context.Context,

		resumeTimeout time.Duration,
		agentVSockPort uint32,
	) (
		waitFunc func() []error,
		closeFunc func() []error,

		msyncFunc func(ctx context.Context) error,
		suspendAndCloseAgentHandlerFunc func(ctx context.Context, resumeTimeout time.Duration) error,

		errs []error,
	),

	errs []error,
) {
	var errsLock sync.Mutex

	internalCtx, cancel := context.WithCancelCause(ctx)
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
					errs = append(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(err)
	}

	vp, firecrackerWait, firecrackerKill, errs := firecracker.StartFirecrackerServer(
		ctx, // We use ctx, not internalCtx here since this resource outlives the function call

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
	for _, err := range errs {
		if err != nil {
			panic(err)
		}
	}

	vmPath = vp

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	go func() {
		defer handleGoroutinePanic()()

		if err := firecrackerWait(); err != nil {
			panic(err)
		}
	}()

	waitFunc = firecrackerWait
	closeFunc = func() error {
		if err := firecrackerKill(); err != nil {
			return err
		}

		if err := waitFunc(); err != nil {
			return err
		}

		_ = os.RemoveAll(filepath.Dir(vmPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

		return nil
	}

	firecrackerClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(vmPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	resumeFunc = func(
		ctx context.Context,

		resumeTimeout time.Duration,
		agentVSockPort uint32,
	) (
		waitFunc func() []error,
		closeFunc func() []error,

		msyncFunc func(ctx context.Context) error,
		suspendAndCloseAgentHandlerFunc func(ctx context.Context, resumeTimeout time.Duration) error,

		errs []error,
	) {
		var errsLock sync.Mutex

		var wg sync.WaitGroup
		defer wg.Wait()

		internalCtx, cancel := context.WithCancelCause(ctx)
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
						errs = append(errs, e)
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

			if err := firecrackerWait(); err != nil {
				panic(err)
			}
		}()

		if err := firecracker.ResumeSnapshot(
			internalCtx,

			firecrackerClient,

			stateName,
			memoryName,
		); err != nil {
			panic(err)
		}

		remote, agentHandlerWait, agentHandlerClose, errs := vsock.CreateNewAgentHandler(
			ctx, // We use ctx, not internalCtx here since this resource outlives the function call

			filepath.Join(vmPath, VSockName),
			agentVSockPort,

			time.Millisecond*100,
			resumeTimeout,
		)

		for _, err := range errs {
			if err != nil {
				panic(err)
			}
		}

		// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
		// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
		go func() {
			defer handleGoroutinePanic()()

			errs := agentHandlerWait()
			for _, err := range errs {
				if err != nil {
					panic(err)
				}
			}
		}()

		waitFunc = agentHandlerWait
		closeFunc = func() []error {
			if errs := agentHandlerClose(); len(errs) > 0 {
				return errs
			}

			return waitFunc()
		}

		resumeCtx, cancelResumeCtx := context.WithTimeout(internalCtx, resumeTimeout)
		defer cancelResumeCtx()

		if err := remote.AfterResume(resumeCtx); err != nil {
			panic(err)
		}

		msyncFunc = func(ctx context.Context) error {
			return firecracker.CreateSnapshot(
				ctx,

				firecrackerClient,

				stateName,
				"",

				firecracker.SnapshotTypeMsync,
			)
		}

		suspendAndCloseAgentHandlerFunc = func(ctx context.Context, resumeTimeout time.Duration) error {
			{
				ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
				defer cancel()

				if err := remote.BeforeSuspend(ctx); err != nil {
					return err
				}
			}

			_ = agentHandlerClose() // Connection needs to be closed before flushing the snapshot

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
