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

	srv     *firecracker.Server
	client  *http.Client
	handler *vsock.Handler
	remote  remotes.AgentRemote

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
	if r.srv == nil {
		if err := os.MkdirAll(r.hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
			return "", err
		}

		r.srv = firecracker.NewServer(
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

		r.wg.Add(1)
		go func() {
			defer r.wg.Done()

			if err := r.srv.Wait(); err != nil {
				r.errs <- err
			}
		}()

		var err error
		r.vmPath, err = r.srv.Open()
		if err != nil {
			return "", err
		}

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

	r.handler = vsock.NewHandler(
		filepath.Join(r.vmPath, VSockName),
		agentVSockPort,
	)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		if err := r.handler.Wait(); err != nil {
			r.errs <- err
		}
	}()

	var err error
	r.remote, err = r.handler.Open(ctx, time.Millisecond*100, resumeTimeout)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()

	return r.remote.AfterResume(ctx)
}

func (r *Runner) Msync(ctx context.Context) error {
	if r.handler == nil {
		return ErrVMNotRunning
	}

	if err := firecracker.FlushSnapshot(r.client, r.stateName, true); err != nil {
		return err
	}

	return nil
}

func (r *Runner) Suspend(ctx context.Context, resumeTimeout time.Duration) error {
	if r.handler == nil {
		return ErrVMNotRunning
	}

	{
		ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
		defer cancel()

		if err := r.remote.BeforeSuspend(ctx); err != nil {
			return err
		}
	}

	_ = r.handler.Close() // Connection needs to be closed before flushing the snapshot

	if err := firecracker.FlushSnapshot(r.client, r.stateName, false); err != nil {
		return err
	}

	r.handler = nil

	return nil
}

func (r *Runner) Close() error {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()

	if r.handler != nil {
		_ = r.handler.Close()
	}

	if r.srv != nil {
		_ = r.srv.Close()
	}

	r.wg.Wait()

	_ = os.RemoveAll(filepath.Dir(r.vmPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	if r.errs != nil {
		close(r.errs)

		r.errs = nil
	}

	return nil
}
