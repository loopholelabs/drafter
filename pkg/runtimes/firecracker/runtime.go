package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sync"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
)

var ErrConfigFileNotFound = errors.New("config file not found")
var ErrCouldNotOpenConfigFile = errors.New("could not open config file")
var ErrCouldNotDecodeConfigFile = errors.New("could not decode config file")
var ErrCouldNotResumeRunner = errors.New("could not resume runner")
var ErrCouldNotRemoveVMDir = errors.New("could not remove vm dir")
var ErrCouldNotResumeSnapshot = errors.New("could not resume snapshot")

type FirecrackerRuntimeProvider[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Log                     types.Logger
	Machine                 *FirecrackerMachine
	HypervisorConfiguration FirecrackerMachineConfig
	StateName               string
	MemoryName              string

	hypervisorCtx    context.Context
	hypervisorCancel context.CancelFunc
	resumeCtx        context.Context
	resumeCancel     context.CancelFunc

	runningLock sync.Mutex
	running     bool

	// RPC Bits
	agent            *ipc.AgentRPC[L, R, G]
	AgentServerLocal L
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

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(resumeCtx, resumeTimeout)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = rp.Machine.ResumeSnapshot(resumeSnapshotAndAcceptCtx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		return err
	}

	rp.running = true

	// Start the RPC stuff...
	rp.agent, err = ipc.StartAgentRPC[L, R](
		rp.Log, path.Join(rp.Machine.VMPath, VSockName),
		packageConfig.AgentVSockPort, rp.AgentServerLocal)
	if err != nil {
		return err
	}

	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(resumeCtx, resumeTimeout)
	defer cancelAfterResumeCtx()

	r, err := rp.agent.GetRemote(afterResumeCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		return err
	}

	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) GetRemote(ctx context.Context) (R, error) {
	return rp.agent.GetRemote(ctx)
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error {
	hypervisorCtx, hypervisorCancel := context.WithCancel(context.TODO())

	rp.hypervisorCtx = hypervisorCtx
	rp.hypervisorCancel = hypervisorCancel

	var err error
	rp.Machine, err = StartFirecrackerMachine(hypervisorCtx, rp.Log, &rp.HypervisorConfiguration)

	if err == nil {
		// Wait for the machine, and relay it to the errChan
		go func() {
			err := rp.Machine.Wait()
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
	if rp.Log != nil {
		rp.Log.Info().Msg("resumed runner Msync")
	}

	err := rp.Machine.CreateSnapshot(ctx, common.DeviceStateName, "", SDKSnapshotTypeMsync)
	if err != nil {
		if rp.Log != nil {
			rp.Log.Error().Err(err).Msg("error in resumed runner Msync")
		}
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) DevicePath() string {
	return rp.Machine.VMPath
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) GetVMPid() int {
	return rp.Machine.VMPid
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Close() error {
	if rp.agent != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing resumed runner")
		}

		// We only need to do this if it hasn't been suspended, but it'll refuse inside Suspend
		err := rp.Suspend(context.TODO(), time.Minute) // TODO. Timeout

		rp.resumeCancel() // We can cancel this context now
		rp.hypervisorCancel()

		err = rp.agent.Close()
		if err != nil {
			return err
		}
	}

	if rp.Machine != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Closing machine")
		}
		err := rp.Machine.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseServer, err)
		}
		/*
		   FOR NOW, DON'T REMOVE DATA
		   		err = os.RemoveAll(filepath.Dir(rp.Machine.VMPath))
		   		if err != nil {
		   			return errors.Join(ErrCouldNotRemoveVMDir, err)
		   		}
		*/
	}
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Suspend(ctx context.Context, suspendTimeout time.Duration) error {
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

	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, suspendTimeout)
	defer cancelSuspendCtx()

	r, err := rp.agent.GetRemote(suspendCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	if err != nil {
		return errors.Join(ErrCouldNotCallBeforeSuspendRPC, err)
	}

	err = rp.agent.Close()
	if err != nil {
		return err
	}

	if rp.Log != nil {
		rp.Log.Debug().Msg("resumedRunner createSnapshot")
	}

	err = rp.Machine.CreateSnapshot(suspendCtx, common.DeviceStateName, "", SDKSnapshotTypeMsyncAndState)
	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	rp.running = false

	return nil
}
