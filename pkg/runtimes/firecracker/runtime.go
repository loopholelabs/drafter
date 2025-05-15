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
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/expose"
	"github.com/loopholelabs/silo/pkg/storage/sources"
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

	memoryLock sync.Mutex

	hypervisorCtx    context.Context
	hypervisorCancel context.CancelFunc

	runningLock sync.Mutex
	running     bool

	RunningCB func(r bool)

	// RPC Bits
	agent            *ipc.AgentRPC[L, R, G]
	AgentServerLocal L
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Resume(ctx context.Context, rescueTimeout time.Duration, errChan chan error) error {
	rp.runningLock.Lock()
	defer rp.runningLock.Unlock()

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

	err = rp.Machine.ResumeSnapshot(ctx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		return err
	}

	rp.setRunning(true)

	// Start the RPC stuff...
	rp.agent, err = ipc.StartAgentRPC[L, R](
		rp.Log, path.Join(rp.Machine.VMPath, VSockName),
		packageConfig.AgentVSockPort, rp.AgentServerLocal)
	if err != nil {
		return err
	}

	// Call after resume RPC
	r, err := rp.agent.GetRemote(ctx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
	err = remote.AfterResume(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) setRunning(r bool) {
	rp.running = r

	if rp.RunningCB != nil {
		rp.RunningCB(r)
	}

	// TODO: Start or stop a ticker to grab soft dirty memory changes...
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

func (rp *FirecrackerRuntimeProvider[L, R, G]) FlushData(ctx context.Context, dg *devicegroup.DeviceGroup) error {
	if rp.Log != nil {
		rp.Log.Info().Msg("Firecracker FlushData")
	}

	if !rp.HypervisorConfiguration.NoMapShared {
		err := rp.Machine.CreateSnapshot(ctx, common.DeviceStateName, "", SDKSnapshotTypeMsync)
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("error in firecracker Msync")
			}
			return errors.Join(ErrCouldNotCreateSnapshot, err)
		}
	} else {
		err := rp.grabMemoryChanges(dg)
		if err != nil {
			return err
		}
	}
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) DevicePath() string {
	return rp.Machine.VMPath
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) GetVMPid() int {
	return rp.Machine.VMPid
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Close(dg *devicegroup.DeviceGroup) error {
	if rp.agent != nil {
		if rp.Log != nil {
			rp.Log.Debug().Msg("Firecracker runtime close")
		}

		// We only need to do this if it hasn't been suspended, but it'll refuse inside Suspend
		err := rp.Suspend(context.TODO(), 10*time.Minute, dg) // TODO. Timeout
		if err != nil {
			return err
		}

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

func (rp *FirecrackerRuntimeProvider[L, R, G]) Suspend(ctx context.Context, suspendTimeout time.Duration, dg *devicegroup.DeviceGroup) error {
	rp.runningLock.Lock()
	defer rp.runningLock.Unlock()

	if !rp.running {
		if rp.Log != nil {
			rp.Log.Debug().Msg("firecracker Suspend called but vm not running")
		}
		return nil
	}

	if rp.Log != nil {
		rp.Log.Debug().Msg("firecracker runtime SuspendAndCloseAgentServer")
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
		rp.Log.Debug().Msg("firecracker runtime CreateSnapshot")
	}

	err = rp.Machine.CreateSnapshot(suspendCtx, common.DeviceStateName, "", SDKSnapshotTypeMsyncAndState)
	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	rp.setRunning(false)

	if rp.HypervisorConfiguration.NoMapShared {
		err = rp.grabMemoryChanges(dg)
		if err != nil {
			return err
		}
	}

	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) grabMemoryChanges(dg *devicegroup.DeviceGroup) error {
	rp.memoryLock.Lock()
	defer rp.memoryLock.Unlock()

	if rp.Log != nil {
		rp.Log.Debug().Msg("Grabbing softDirty memory changes")
	}
	// Do a softDirty memory read here, and write it to the memory device.
	// Currently, we do the write through NBD, but we could shortcut later if we wish.
	pm := expose.NewProcessMemory(rp.Machine.VMPid)
	di := dg.GetDeviceInformationByName("memory")
	addrStart, addrEnd, err := pm.GetMemoryRange("/memory")
	if err != nil {
		return err
	}
	if rp.Log != nil {
		rp.Log.Debug().Uint64("addrEnd", addrEnd).Uint64("addrStart", addrStart).Msg("SoftDirty memory changes")
	}

	lockcb := func() error {
		err := pm.PauseProcess()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not pause process")
			}
		}
		return err
	}

	unlockcb := func() error {
		err := pm.ResumeProcess()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not resume process")
			}
		}
		return err
	}

	ranges, err := pm.ReadSoftDirtyMemoryRangeList(addrStart, addrEnd, lockcb, unlockcb)
	if err != nil {
		return err
	}

	// Copy to the Silo provider
	n, err := pm.CopyMemoryRanges(addrStart, ranges, di.Exp.GetProvider())
	if err != nil {
		return err
	}
	if rp.Log != nil {
		rp.Log.Debug().Uint64("bytes", n).Msg("SoftDirty copied memory to silo provider")
	}

	// Kinda hacky. We also write it through the nbd device, so hash verify works from there...
	// We should
	prov, err := sources.NewFileStorage(path.Join("/dev", di.Exp.Device()), int64(di.Size))
	if err != nil {
		return err
	}

	n, err = pm.CopyMemoryRanges(addrStart, ranges, prov)
	if err != nil {
		return err
	}
	if rp.Log != nil {
		rp.Log.Info().Uint64("bytes", n).Msg("SoftDirty copied memory")
	}
	err = prov.Close()
	if err != nil {
		return err
	}

	return nil
}
