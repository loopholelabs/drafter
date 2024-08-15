package runner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/drafter/internal/firecracker"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

type SnapshotLoadConfiguration struct {
	ExperimentalMapPrivate bool

	ExperimentalMapPrivateStateOutput  string
	ExperimentalMapPrivateMemoryOutput string
}

type Runner struct {
	VMPath string
	VMPid  int

	Wait  func() error
	Close func() error

	ongoingResumeWg   sync.WaitGroup
	firecrackerClient *http.Client

	hypervisorConfiguration snapshotter.HypervisorConfiguration

	stateName,
	memoryName string

	server *firecracker.FirecrackerServer

	rescueCtx context.Context
}

func StartRunner(
	hypervisorCtx context.Context,
	rescueCtx context.Context,

	hypervisorConfiguration snapshotter.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	runner *Runner,

	errs error,
) {
	runner = &Runner{
		Wait:  func() error { return nil },
		Close: func() error { return nil },

		hypervisorConfiguration: hypervisorConfiguration,

		stateName:  stateName,
		memoryName: memoryName,

		rescueCtx: rescueCtx,
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
		panic(errors.Join(snapshotter.ErrCouldNotCreateChrootBaseDirectory, err))
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
		panic(errors.Join(snapshotter.ErrCouldNotStartFirecrackerServer, err))
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
