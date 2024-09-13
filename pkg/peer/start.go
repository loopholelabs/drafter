package peer

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

type Peer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	VMPath string
	VMPid  int

	Wait  func() error
	Close func() error

	hypervisorCtx context.Context

	runner *runner.Runner[L, R, G]
}

func StartPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	hypervisorCtx context.Context,
	rescueCtx context.Context,

	hypervisorConfiguration snapshotter.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	peer *Peer[L, R, G],

	errs error,
) {
	peer = &Peer[L, R, G]{
		hypervisorCtx: hypervisorCtx,

		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
	}

	goroutineManager := manager.NewGoroutineManager(
		hypervisorCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	var err error
	peer.runner, err = runner.StartRunner[L, R](
		hypervisorCtx,
		rescueCtx,

		hypervisorConfiguration,

		stateName,
		memoryName,
	)

	// We set both of these even if we return an error since we need to have a way to wait for rescue operations to complete
	peer.Wait = peer.runner.Wait
	peer.Close = func() error {
		if err := peer.runner.Close(); err != nil {
			return err
		}

		return peer.Wait()
	}

	if err != nil {
		panic(errors.Join(ErrCouldNotStartRunner, err))
	}

	peer.VMPath = peer.runner.VMPath
	peer.VMPid = peer.runner.VMPid

	// We don't track this because we return the wait function
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := peer.runner.Wait(); err != nil {
			panic(err)
		}
	})

	return
}
