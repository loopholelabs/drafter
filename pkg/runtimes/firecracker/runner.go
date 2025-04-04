package firecracker

import (
	"context"
	"errors"
	"sync"

	"github.com/loopholelabs/logging/types"
)

type Runner struct {
	Machine *FirecrackerMachine

	ongoingResumeWg sync.WaitGroup
	rescueCtx       context.Context
}

func StartRunner(log types.Logger, hypervisorCtx context.Context, rescueCtx context.Context,
	hypervisorConfiguration FirecrackerMachineConfig) (*Runner, error) {

	runner := &Runner{
		rescueCtx: rescueCtx,
	}

	firecrackerCtx, cancelFirecrackerCtx := context.WithCancel(rescueCtx)
	// We use `rescueContext` here since this simply intercepts `hypervisorCtx`
	// and then waits for `rescueCtx` or the rescue operation to complete
	go func() {
		<-hypervisorCtx.Done()
		runner.ongoingResumeWg.Wait()
		cancelFirecrackerCtx()
	}()

	var err error
	runner.Machine, err = StartFirecrackerMachine(firecrackerCtx, log, &hypervisorConfiguration)
	if err != nil {
		return nil, errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}

	return runner, nil
}
