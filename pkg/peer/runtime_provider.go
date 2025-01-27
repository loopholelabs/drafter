package peer

import (
	"context"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/logging/types"
)

type RuntimeProvider[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Log                     types.Logger
	Remote                  ipc.AgentServerRemote[R]
	Runner                  *runner.Runner[L, R, G]
	ResumedRunner           *runner.ResumedRunner[L, R, G]
	HypervisorConfiguration snapshotter.HypervisorConfiguration
	StateName               string
	MemoryName              string

	hypervisorCtx    context.Context
	hypervisorCancel context.CancelFunc
	resumeCtx        context.Context
	resumeCancel     context.CancelFunc

	AgentServerLocal L
	AgentServerHooks ipc.AgentServerAcceptHooks[R, G]

	SnapshotLoadConfiguration runner.SnapshotLoadConfiguration
}

type RuntimeProviderIfc interface {
	Start(ctx context.Context, rescueCtx context.Context) error
	Close() error
	DevicePath() string
	GetVMPid() int
	SuspendAndCloseAgentServer(ctx context.Context, timeout time.Duration) error
	FlushData(ctx context.Context) error
	Resume(resumeTimeout time.Duration, rescueTimeout time.Duration, vsockPort uint32) error
}

func (rp *RuntimeProvider[L, R, G]) Resume(resumeTimeout time.Duration, rescueTimeout time.Duration, vsockPort uint32) error {
	resumeCtx, resumeCancel := context.WithCancel(context.TODO())
	rp.resumeCtx = resumeCtx
	rp.resumeCancel = resumeCancel

	var err error
	rp.ResumedRunner, err = rp.Runner.Resume(
		resumeCtx,
		resumeTimeout,
		rescueTimeout,
		vsockPort,
		rp.AgentServerLocal,
		rp.AgentServerHooks,
		rp.SnapshotLoadConfiguration,
	)
	return err
}

func (rp *RuntimeProvider[L, R, G]) Start(ctx context.Context, rescueCtx context.Context) error {
	hypervisorCtx, hypervisorCancel := context.WithCancel(context.TODO())

	rp.hypervisorCtx = hypervisorCtx
	rp.hypervisorCancel = hypervisorCancel

	run, err := runner.StartRunner[L, R](
		hypervisorCtx,
		rescueCtx,
		rp.HypervisorConfiguration,
		rp.StateName,
		rp.MemoryName,
	)
	rp.Runner = run
	return err
}

func (rp *RuntimeProvider[L, R, G]) FlushData(ctx context.Context) error {
	return rp.ResumedRunner.Msync(ctx)
}

func (rp *RuntimeProvider[L, R, G]) DevicePath() string {
	return rp.Runner.VMPath
}

func (rp *RuntimeProvider[L, R, G]) GetVMPid() int {
	return rp.Runner.VMPid
}

func (rp *RuntimeProvider[L, R, G]) Close() error {
	// TODO: Correct?
	if rp.ResumedRunner != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing resumed runner")
		}

		err := rp.SuspendAndCloseAgentServer(context.TODO(), time.Minute) // TODO

		rp.resumeCancel() // We can cancel this context now
		rp.hypervisorCancel()

		err = rp.ResumedRunner.Close()
		if err != nil {
			return err
		}
		err = rp.ResumedRunner.Wait()
		if err != nil {
			return err
		}
	} else if rp.Runner != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing runner")
		}
		err := rp.Runner.Close()
		if err != nil {
			return err
		}
		err = rp.Runner.Wait()
		if err != nil {
			return err
		}
	}
	return nil
}

func (rp *RuntimeProvider[L, R, G]) SuspendAndCloseAgentServer(ctx context.Context, resumeTimeout time.Duration) error {
	if rp.Log != nil {
		rp.Log.Debug().Msg("resumedPeer.SuspendAndCloseAgentServer")
	}
	err := rp.ResumedRunner.SuspendAndCloseAgentServer(
		ctx,
		resumeTimeout,
	)
	if err != nil {
		if rp.Log != nil {
			rp.Log.Warn().Err(err).Msg("error from resumedPeer.SuspendAndCloseAgentServer")
		}
	}
	return err
}
