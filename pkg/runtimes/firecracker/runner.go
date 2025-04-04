package firecracker

import (
	"context"
	"errors"

	"github.com/loopholelabs/logging/types"
)

type Runner struct {
	Machine *FirecrackerMachine
}

func StartRunner(log types.Logger, hypervisorCtx context.Context, hypervisorConfiguration FirecrackerMachineConfig) (*Runner, error) {

	runner := &Runner{}

	var err error
	runner.Machine, err = StartFirecrackerMachine(hypervisorCtx, log, &hypervisorConfiguration)
	if err != nil {
		return nil, errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}

	return runner, nil
}
