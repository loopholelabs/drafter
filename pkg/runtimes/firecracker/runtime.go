package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
)

var ErrConfigFileNotFound = errors.New("config file not found")
var ErrCouldNotOpenConfigFile = errors.New("could not open config file")
var ErrCouldNotDecodeConfigFile = errors.New("could not decode config file")
var ErrCouldNotResumeRunner = errors.New("could not resume runner")
var ErrCouldNotRemoveVMDir = errors.New("could not remove vm dir")

type FirecrackerRuntimeProvider[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Log                     types.Logger
	Runner                  *Runner
	ResumedRunner           *ResumedRunner[L, R, G]
	HypervisorConfiguration FirecrackerMachineConfig
	StateName               string
	MemoryName              string

	hypervisorCtx    context.Context
	hypervisorCancel context.CancelFunc
	resumeCtx        context.Context
	resumeCancel     context.CancelFunc

	SnapshotLoadConfiguration SnapshotLoadConfiguration

	runningLock sync.Mutex
	running     bool

	// RPC Bits
	Remote           R
	AgentServerLocal L
	AgentServerHooks ipc.AgentServerAcceptHooks[R, G]
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Resume(resumeTimeout time.Duration, rescueTimeout time.Duration, errChan chan error) error {
	rp.runningLock.Lock()
	defer rp.runningLock.Unlock()

	resumeCtx, resumeCancel := context.WithCancel(context.TODO())
	rp.resumeCtx = resumeCtx
	rp.resumeCancel = resumeCancel

	// Read from the config device
	configFileData, err := os.ReadFile(path.Join(rp.DevicePath(), common.DeviceConfigName))
	if err != nil {
		return errors.Join(ErrCouldNotOpenConfigFile, err)
	}

	// Find the first 0 byte...
	firstZero := 0
	for i := 0; i < len(configFileData); i++ {
		if configFileData[i] == 0 {
			firstZero = i
			break
		}
	}
	configFileData = configFileData[:firstZero]

	var packageConfig PackageConfiguration
	if err := json.Unmarshal(configFileData, &packageConfig); err != nil {
		return errors.Join(ErrCouldNotDecodeConfigFile, err)
	}

	rp.ResumedRunner, err = Resume[L, R, G](
		rp.Runner.Machine,
		resumeCtx,
		resumeTimeout,
		rescueTimeout,
		packageConfig.AgentVSockPort,
		rp.AgentServerLocal,
		rp.AgentServerHooks,
		rp.SnapshotLoadConfiguration,
	)
	if err != nil {
		return err
	}

	rp.Remote = *rp.ResumedRunner.rpc.Remote

	go func() {
		err := rp.ResumedRunner.Wait()
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	rp.running = true
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error {
	hypervisorCtx, hypervisorCancel := context.WithCancel(context.TODO())

	rp.hypervisorCtx = hypervisorCtx
	rp.hypervisorCancel = hypervisorCancel

	run, err := StartRunner(
		rp.Log,
		hypervisorCtx,
		rp.HypervisorConfiguration,
	)
	if err == nil {
		rp.Runner = run
		go func() {
			err := run.Machine.Wait()
			if err != nil {
				select {
				case errChan <- err:
				default:
				}
			}
		}()
	}
	return err
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) FlushData(ctx context.Context) error {
	return rp.ResumedRunner.Msync(ctx)
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) DevicePath() string {
	return rp.Runner.Machine.VMPath
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) GetVMPid() int {
	return rp.Runner.Machine.VMPid
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Close() error {
	if rp.ResumedRunner != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing resumed runner")
		}

		// We only need to do this if it hasn't been suspended, but it'll refuse inside Suspend
		err := rp.Suspend(context.TODO(), time.Minute) // TODO. Timeout

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
		rp.ResumedRunner = nil // Just to make sure if there's further calls to Close

	} else if rp.Runner != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing machine")
		}
		err := rp.Runner.Machine.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseServer, err)
		}

		err = os.RemoveAll(filepath.Dir(rp.Runner.Machine.VMPath))
		if err != nil {
			return errors.Join(ErrCouldNotRemoveVMDir, err)
		}
	}
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Suspend(ctx context.Context, resumeTimeout time.Duration) error {
	rp.runningLock.Lock()
	defer rp.runningLock.Unlock()

	if !rp.running {
		if rp.Log != nil {
			rp.Log.Debug().Msg("firecracker Suspend called but vm not running")
		}
		return nil
	}

	if rp.Log != nil {
		rp.Log.Debug().Msg("firecracker SuspendAndCloseAgentServer")
	}
	err := rp.ResumedRunner.SuspendAndCloseAgentServer(
		ctx,
		resumeTimeout,
	)
	if err != nil {
		if rp.Log != nil {
			rp.Log.Warn().Err(err).Msg("error from SuspendAndCloseAgentServer")
		}
	} else {
		rp.running = false
	}
	return err
}
