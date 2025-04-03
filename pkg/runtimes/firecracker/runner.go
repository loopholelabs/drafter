package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/logging/types"
)

var (
	ErrCouldNotRemoveVMDir = errors.New("could not remove VM directory")
)

type Runner struct {
	log                     types.Logger
	VMPath                  string
	VMPid                   int
	ongoingResumeWg         sync.WaitGroup
	hypervisorConfiguration FirecrackerMachineConfig
	server                  *FirecrackerMachine
	rescueCtx               context.Context
}

func (r *Runner) Wait() error {
	if r.server != nil {
		return r.server.Wait()
	}
	return nil
}

func (r *Runner) Close() error {
	if r.server != nil {
		err := r.server.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseServer, err)
		}

		err = os.RemoveAll(filepath.Dir(r.VMPath))
		if err != nil {
			return errors.Join(ErrCouldNotRemoveVMDir, err)
		}
	}
	return nil
}

func StartRunner(log types.Logger, hypervisorCtx context.Context, rescueCtx context.Context,
	hypervisorConfiguration FirecrackerMachineConfig) (*Runner, error) {

	runner := &Runner{
		log:                     log,
		hypervisorConfiguration: hypervisorConfiguration,
		rescueCtx:               rescueCtx,
	}

	if log != nil {
		log.Info().Msg("firecracker starting runner")
	}

	err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm)
	if err != nil {
		return nil, errors.Join(ErrCouldNotCreateChrootBaseDirectory, err)
	}

	firecrackerCtx, cancelFirecrackerCtx := context.WithCancel(rescueCtx)
	// We use `rescueContext` here since this simply intercepts `hypervisorCtx`
	// and then waits for `rescueCtx` or the rescue operation to complete
	go func() {
		<-hypervisorCtx.Done()
		runner.ongoingResumeWg.Wait()
		cancelFirecrackerCtx()
	}()

	runner.server, err = StartFirecrackerMachine(firecrackerCtx, log, &hypervisorConfiguration)
	if err != nil {
		return nil, errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}

	runner.VMPath = runner.server.VMPath
	runner.VMPid = runner.server.VMPid

	if log != nil {
		log.Info().Str("vmpath", runner.VMPath).Int("vmpid", runner.VMPid).Msg("firecracker runner started")
	}

	return runner, nil
}
