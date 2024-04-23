package roles

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/firecracker"
	"github.com/loopholelabs/drafter/pkg/remotes"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

const (
	VSockName = "drafter.drftsock"
)

var (
	ErrVMNotRunning = errors.New("vm not running")
)

type Runner struct {
	hypervisorConfiguration config.HypervisorConfiguration

	stateName  string
	memoryName string

	firecrackerKill   func() error
	client            *http.Client
	agentHandlerClose func() error
	remote            remotes.AgentRemote

	vmPath string

	wg sync.WaitGroup

	closeLock sync.Mutex

	errs chan error
}

func NewRunner(
	hypervisorConfiguration config.HypervisorConfiguration,

	stateName string,
	memoryName string,
) *Runner {
	return &Runner{
		hypervisorConfiguration: hypervisorConfiguration,

		stateName:  stateName,
		memoryName: memoryName,

		wg:   sync.WaitGroup{},
		errs: make(chan error),
	}
}

func (r *Runner) Wait() error {
	for err := range r.errs {
		if err != nil {
			return err
		}
	}

	r.wg.Wait()

	return nil
}

func (r *Runner) Open() (string, error) {
	if r.firecrackerKill == nil {
		if err := os.MkdirAll(r.hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
			return "", err
		}

		// TODO: Integrate with new context handling logic
		vmPath, firecrackerWait, firecrackerKill, errs := firecracker.StartFirecrackerServer(
			context.Background(),

			r.hypervisorConfiguration.FirecrackerBin,
			r.hypervisorConfiguration.JailerBin,

			r.hypervisorConfiguration.ChrootBaseDir,

			r.hypervisorConfiguration.UID,
			r.hypervisorConfiguration.GID,

			r.hypervisorConfiguration.NetNS,
			r.hypervisorConfiguration.NumaNode,
			r.hypervisorConfiguration.CgroupVersion,

			r.hypervisorConfiguration.EnableOutput,
			r.hypervisorConfiguration.EnableInput,
		)
		for _, err := range errs {
			if err != nil {
				return "", err
			}
		}

		r.vmPath = vmPath
		r.firecrackerKill = firecrackerKill

		r.wg.Add(1)
		go func() {
			defer r.wg.Done()

			if err := firecrackerWait(); err != nil {
				r.errs <- err
			}
		}()

		r.client = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return net.Dial("unix", filepath.Join(r.vmPath, firecracker.FirecrackerSocketName))
				},
			},
		}
	}

	return r.vmPath, nil
}

func (r *Runner) Resume(
	ctx context.Context,
	resumeTimeout time.Duration,
	agentVSockPort uint32,
) error {
	if err := firecracker.ResumeSnapshot(
		r.client,

		r.stateName,
		r.memoryName,
	); err != nil {
		return err
	}

	remote, agentHandlerWait, agentHandlerClose, errs := vsock.CreateNewAgentHandler(
		ctx,

		filepath.Join(r.vmPath, VSockName),
		agentVSockPort,

		time.Millisecond*100,
		resumeTimeout,
	)
	for _, err := range errs {
		if err != nil {
			panic(err)
		}
	}

	r.remote = remote
	r.agentHandlerClose = agentHandlerClose

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		errs := agentHandlerWait()
		for _, err := range errs {
			if err != nil {
				r.errs <- err
			}
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()

	return r.remote.AfterResume(ctx)
}

func (r *Runner) Msync(ctx context.Context) error {
	if r.agentHandlerClose == nil {
		return ErrVMNotRunning
	}

	if err := firecracker.CreateSnapshot(
		r.client,

		r.stateName,
		"",

		firecracker.SnapshotTypeMsync,
	); err != nil {
		return err
	}

	return nil
}

func (r *Runner) Suspend(ctx context.Context, resumeTimeout time.Duration) error {
	if r.agentHandlerClose == nil {
		return ErrVMNotRunning
	}

	{
		ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
		defer cancel()

		if err := r.remote.BeforeSuspend(ctx); err != nil {
			return err
		}
	}

	_ = r.agentHandlerClose() // Connection needs to be closed before flushing the snapshot

	if err := firecracker.CreateSnapshot(
		r.client,

		r.stateName,
		"",

		firecracker.SnapshotTypeMsyncAndState,
	); err != nil {
		return err
	}

	r.agentHandlerClose = nil

	return nil
}

func (r *Runner) Close() error {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()

	if r.agentHandlerClose != nil {
		_ = r.agentHandlerClose()
	}

	if r.firecrackerKill != nil {
		_ = r.firecrackerKill()
	}

	r.wg.Wait()

	_ = os.RemoveAll(filepath.Dir(r.vmPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	if r.errs != nil {
		close(r.errs)

		r.errs = nil
	}

	return nil
}
