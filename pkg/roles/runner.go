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

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/network"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/loopholelabs/architekt/pkg/vsock"
)

const (
	VSockName = "architect.arksock"
)

var (
	ErrVMNotRunning = errors.New("vm not running")
)

type HypervisorConfiguration struct {
	FirecrackerBin string

	Verbose      bool
	EnableOutput bool
	EnableInput  bool
}

type NetworkConfiguration struct {
	HostInterface   string
	HostMAC         string
	BridgeInterface string
}

type AgentConfiguration struct {
	AgentVSockPort uint32
}

type Runner struct {
	hypervisorConfiguration HypervisorConfiguration
	networkConfiguration    NetworkConfiguration
	agentConfiguration      AgentConfiguration

	packageDir            string
	mount                 *utils.Mount
	tap                   *network.TAP
	firecrackerSocketDir  string
	firecrackerSocketPath string
	srv                   *firecracker.Server
	client                *http.Client
	handler               *vsock.Handler
	peer                  services.AgentRemote

	wg   sync.WaitGroup
	errs chan error
}

func NewRunner(
	hypervisorConfiguration HypervisorConfiguration,
	networkConfiguration NetworkConfiguration,
	agentConfiguration AgentConfiguration,
) *Runner {
	return &Runner{
		hypervisorConfiguration: hypervisorConfiguration,
		networkConfiguration:    networkConfiguration,
		agentConfiguration:      agentConfiguration,

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

func (r *Runner) Open() error {
	var err error
	r.packageDir, err = os.MkdirTemp("", "")
	if err != nil {
		return err
	}

	r.tap = network.NewTAP(
		r.networkConfiguration.HostInterface,
		r.networkConfiguration.HostMAC,
		r.networkConfiguration.BridgeInterface,
	)
	if err := r.tap.Open(); err != nil {
		return err
	}

	r.firecrackerSocketDir, err = os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	r.firecrackerSocketPath = filepath.Join(r.firecrackerSocketDir, "firecracker.sock")

	r.client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", r.firecrackerSocketPath)
			},
		},
	}

	return nil
}

func (r *Runner) Resume(ctx context.Context, packageDevicePath string) error {
	r.mount = utils.NewMount(packageDevicePath, r.packageDir)
	if err := r.mount.Open(); err != nil {
		return err
	}

	if r.srv == nil {
		r.srv = firecracker.NewServer(
			r.hypervisorConfiguration.FirecrackerBin,
			r.firecrackerSocketPath,
			r.packageDir,
			r.hypervisorConfiguration.Verbose,
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

		if err := r.srv.Open(); err != nil {
			return err
		}
	}

	if err := firecracker.ResumeSnapshot(r.client); err != nil {
		return err
	}

	r.handler = vsock.NewHandler(
		filepath.Join(r.packageDir, VSockName),
		r.agentConfiguration.AgentVSockPort,
		time.Second*10,
	)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		if err := r.handler.Wait(); err != nil {
			r.errs <- err
		}
	}()

	var err error
	r.peer, err = r.handler.Open(ctx, time.Millisecond*100, time.Second*10)
	if err != nil {
		return err
	}

	return r.peer.AfterResume(ctx)
}

func (r *Runner) Suspend(ctx context.Context) error {
	if r.handler == nil {
		return ErrVMNotRunning
	}

	if err := r.peer.BeforeSuspend(ctx); err != nil {
		return err
	}

	_ = r.handler.Close() // Connection needs to be closed before flushing the snapshot

	if err := firecracker.FlushSnapshot(r.client); err != nil {
		return err
	}

	r.handler = nil

	return nil
}

func (r *Runner) Close() error {
	if r.handler != nil {
		_ = r.handler.Close()
	}

	if r.srv != nil {
		_ = r.srv.Close()
	}

	r.wg.Wait()

	_ = os.Remove(filepath.Join(r.packageDir, VSockName))

	_ = os.RemoveAll(r.firecrackerSocketDir)

	if r.tap != nil {
		_ = r.tap.Close()
	}

	if r.mount != nil {
		_ = r.mount.Close()
	}

	_ = os.RemoveAll(r.packageDir)

	if r.errs != nil {
		close(r.errs)

		r.errs = nil
	}

	return nil
}
