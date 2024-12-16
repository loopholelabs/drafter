package peer

import (
	"errors"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/packager"
)

type DeviceStatus struct {
	TotalCycles                   int
	CycleThrottle                 time.Duration
	MaxDirtyBlocks                int
	CyclesBelowDirtyBlockTreshold int
	MinCycles                     int
	MaxCycles                     int
	Ready                         bool
}

type DirtyManager struct {
	VMState                    *VMStateMgr
	Devices                    map[string]*DeviceStatus
	ReadyDevices               map[string]*DeviceStatus
	ReadyDevicesLock           sync.Mutex
	AuthorityTransfer          func()
	AuthorityTransferLock      sync.Mutex
	AuthorityTransferCompleted bool
}

func NewDirtyManager(vmState *VMStateMgr, devices map[string]*DeviceStatus, authorityTransfer func()) *DirtyManager {
	return &DirtyManager{
		VMState:           vmState,
		Devices:           devices,
		ReadyDevices:      make(map[string]*DeviceStatus),
		AuthorityTransfer: authorityTransfer,
	}
}

func (dm *DirtyManager) PreGetDirty(name string) error {
	// If the VM is still running, do an Msync for the memory...
	if !dm.VMState.CheckSuspendedVM() && name == packager.MemoryName {
		err := dm.VMState.Msync()
		if err != nil {
			return errors.Join(ErrCouldNotMsyncRunner, err)
		}
	}
	return nil
}

func (dm *DirtyManager) PostGetDirty(name string, blocks []uint) (bool, error) {
	// If there were no dirty blocks, and the VM is stopped, return false (finish doing dirty sync)
	if len(blocks) == 0 && dm.VMState.CheckSuspendedVM() {
		return false, nil
	}
	return true, nil
}

func (dm *DirtyManager) PostMigrateDirty(name string, blocks []uint) (bool, error) {
	di := dm.Devices[name]
	time.Sleep(di.CycleThrottle)
	if dm.VMState.CheckSuspendedVM() {
		return true, nil // VM is suspended, all done.
	}
	di.TotalCycles++

	if len(blocks) < di.MaxDirtyBlocks {
		di.CyclesBelowDirtyBlockTreshold++
		if di.CyclesBelowDirtyBlockTreshold > di.MinCycles {
			if !di.Ready {
				di.Ready = true
				dm.ReadyDevicesLock.Lock()
				dm.ReadyDevices[name] = di
				dm.ReadyDevicesLock.Unlock()
			}
		}
	} else if di.TotalCycles > di.MaxCycles {
		if !di.Ready {
			di.Ready = true
			dm.ReadyDevicesLock.Lock()
			dm.ReadyDevices[name] = di
			dm.ReadyDevicesLock.Unlock()
		}
	} else {
		di.CyclesBelowDirtyBlockTreshold = 0
	}

	// If all devices are ready, do the authority transfer once...
	dm.ReadyDevicesLock.Lock()
	readyDevices := len(dm.ReadyDevices)
	dm.ReadyDevicesLock.Unlock()

	if readyDevices == len(dm.Devices) {
		dm.AuthorityTransferLock.Lock()
		c := dm.AuthorityTransferCompleted
		if !c {
			dm.AuthorityTransferCompleted = true
			dm.AuthorityTransferLock.Unlock()

			// Do the SuspsnedAndMsync, and then the AuthorityTransfer callback
			err := dm.VMState.SuspendAndMsync()
			if err != nil {
				return true, errors.Join(mounter.ErrCouldNotSuspendAndMsyncVM, err)
			}
			dm.AuthorityTransfer()
		} else {
			dm.AuthorityTransferLock.Unlock()
		}
	}
	return true, nil
}
