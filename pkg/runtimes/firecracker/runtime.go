package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/memory"
	"github.com/loopholelabs/silo/pkg/storage/modules"
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
	GrabInterval            time.Duration

	memoryLock sync.Mutex

	hypervisorCtx    context.Context
	hypervisorCancel context.CancelFunc

	runningLock sync.Mutex
	running     bool

	RunningCB func(r bool)

	// Memory grabber
	GrabMemory   bool // If true, we will grab memory from /proc/<pid>/mem instead of using map_shared
	GrabFailsafe bool // If true, we do a failsafe memory grab instead of using the soft_dirty mechanism

	GrabUpdateDirty  bool // If true, we update dirty ranges
	GrabUpdateMemory bool // If true, we update the memory content

	grabberCtx    context.Context
	grabberCancel context.CancelFunc
	grabberWg     sync.WaitGroup
	grabberProv   storage.Provider

	DirectMemory          bool // If true, we're going to go directly to /proc/<pid>/mem at migration time, and for S3 assist
	DirectMemorySyncDirty *dirtytracker.Remote
	DirectMemoryDirty     *dirtytracker.Remote

	dg *devicegroup.DeviceGroup

	// RPC Bits
	agent            *ipc.AgentRPC[L, R, G]
	AgentServerLocal L
}

const resumeMaxRetries = 10
const resumeRetrySleep = 1 * time.Second
const resumeRetryTimeout = 30 * time.Second

func (rp *FirecrackerRuntimeProvider[L, R, G]) Resume(ctx context.Context, rescueTimeout time.Duration, dg *devicegroup.DeviceGroup, errChan chan error) error {
	resumeCtime := time.Now()
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

	if rp.DirectMemory {
		di := dg.GetDeviceInformationByName(common.DeviceMemoryName)
		// Find out which blocks we should mark as dirty if they came in from a migration.
		unreq := di.DirtyRemote.GetUnrequiredBlocks()
		blocks := make([]bool, di.NumBlocks)
		for _, b := range unreq {
			blocks[b] = true
		}
		di.DirtyRemote.TrackAt(int64(di.Size), 0) // Track entire memory, since we will be only doing dirty
		for b, un := range blocks {
			if !un {
				offset := b * int(di.BlockSize)
				// Mark the block as dirty...
				di.DirtyRemote.MarkDirty(int64(offset), int64(di.BlockSize))
			}
		}
		if rp.Log != nil {
			rp.Log.Info().Int("blocks", len(unreq)).Msg("Marked blocks dirty from incoming memory")
		}
		di.UseAltSourcesInDirty = true // Do not clear alt sources (S3). We are *ONLY* doing dirty transfer here.
		rp.DirectMemoryDirty = di.DirtyRemote
	}

	err = rp.Machine.ResumeSnapshot(ctx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		return err
	}

	resumeMachineTook := time.Since(resumeCtime)

	// Set things up for DirectMemory access if required. NB This can only be done *after* the resume
	if rp.DirectMemory {
		di := dg.GetDeviceInformationByName(common.DeviceMemoryName)
		// At migration time, we're going to tell Silo it doesn't need to sent anything up front. We'll only be sending dirty
		unrequiredBlocks := make([]uint, di.NumBlocks)
		for i := 0; i < int(di.NumBlocks); i++ {
			unrequiredBlocks[i] = uint(i)
		}

		memProv, err := memory.NewProcessMemoryStorage(rp.Machine.VMPid, "/memory", func() []uint {
			return unrequiredBlocks
		})
		if err != nil {
			return err
		}

		di.DirtyRemote.SetRemoteReadProv(memProv) // Read from the memory directly when migrating

		// Tell the S3 Sync system that it should read directly from the direct memory prov.
		syncInfoRet := storage.SendSiloEvent(di.Prov, storage.EventSyncInfo, nil)
		if len(syncInfoRet) == 1 {
			syncInfo := syncInfoRet[0].([]*device.SyncInfo)
			if len(syncInfo) == 1 {
				syncInfo[0].DirtyRemote.SetRemoteReadProv(memProv)
				rp.DirectMemorySyncDirty = syncInfo[0].DirtyRemote
				if rp.Log != nil {
					rp.Log.Info().Msg("Setup memory S3 source to point to proc mem")
				}
			}
		}
	}

	// Setup the grabber provider
	di := dg.GetDeviceInformationByName(common.DeviceMemoryName)
	rp.grabberProv = di.Exp.GetProvider()

	// Store the dg
	rp.dg = dg

	rp.setRunning(true)

	rpcCtime := time.Now()
	// Start the RPC stuff...
	rp.agent, err = ipc.StartAgentRPC[L, R](
		rp.Log, path.Join(rp.Machine.VMPath, VSockName),
		packageConfig.AgentVSockPort, rp.AgentServerLocal)
	if err != nil {
		return err
	}

	resumeRetries := 0
	for {
		resumeRetries++
		if resumeRetries >= resumeMaxRetries {
			if rp.Log != nil {
				rp.Log.Error().
					Str("vmpath", rp.Machine.VMPath).
					Msg("Resume fc vm failed")
			}
			return ErrCouldNotResumeRunner
		}
		afterResumeCtx, afterResumeCancel := context.WithTimeout(ctx, resumeRetryTimeout)
		r, err := rp.agent.GetRemote(afterResumeCtx)
		if err == nil {
			remote := *(*ipc.AgentServerRemote[G])(unsafe.Pointer(&r))
			err = remote.AfterResume(afterResumeCtx)
			afterResumeCancel()
			if err == nil {
				break // Success path
			}
		} else {
			afterResumeCancel()
		}
		if rp.Log != nil {
			rp.Log.Info().
				Err(err).
				Str("vmpath", rp.Machine.VMPath).
				Msg("Resume RPC call failed.")
		}
		time.Sleep(resumeRetrySleep)
	}

	rpcCallTook := time.Since(rpcCtime)

	if rp.Log != nil {
		// Show some stats on resume timings...
		rp.Log.Info().
			Int64("resumeMachineMs", resumeMachineTook.Milliseconds()).
			Int64("rpcCallMs", rpcCallTook.Milliseconds()).
			Int64("timeMs", time.Since(resumeCtime).Milliseconds()).
			Int("retries", resumeRetries).
			Str("vmpath", rp.Machine.VMPath).
			Msg("Resumed fc vm")
	}

	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) setRunning(r bool) {
	if rp.running == r {
		return // No change. Ignore it
	}

	rp.running = r

	if rp.RunningCB != nil {
		rp.RunningCB(r)
	}

	if rp.GrabInterval != 0 {
		if r {
			// Setup the grabber
			rp.grabberCtx, rp.grabberCancel = context.WithCancel(context.Background())
			rp.grabberWg.Add(1)
			go func() {
				ticker := time.NewTicker(rp.GrabInterval)
				defer ticker.Stop()
				for {
					select {
					case <-rp.grabberCtx.Done():
						rp.grabberWg.Done()
						if rp.Log != nil {
							rp.Log.Info().Msg("memory grabber finished")
						}
						return
					case <-ticker.C:
						err := rp.grabMemoryChanges()
						if err != nil {
							if rp.Log != nil {
								// NB: This isn't critical, but it may still signal up an issue.
								rp.Log.Error().Err(err).Msg("could not grab periodic memory changes")
							}
						}

					}
				}
			}()
		} else {
			// Cancel the grabber, and wait for it to finish
			rp.grabberCancel()
			rp.grabberWg.Wait()
		}
	}
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
	ctime := time.Now()
	defer func() {
		if rp.Log != nil {
			rp.Log.Info().Int64("ms", time.Since(ctime).Milliseconds()).Msg("Timing FlushData")
		}
	}()

	if rp.Log != nil {
		rp.Log.Info().Msg("Firecracker FlushData")
	}

	if rp.GrabMemory {
		err := rp.grabMemoryChanges()
		if err != nil {
			return err
		}
	} else {
		err := rp.Machine.CreateSnapshot(ctx, common.DeviceStateName, "", SDKSnapshotTypeMsync)
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("error in firecracker Msync")
			}
			return errors.Join(ErrCouldNotCreateSnapshot, err)
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
		rp.GrabMemory = false                                 // We don't care.
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

		err = os.RemoveAll(filepath.Dir(rp.Machine.VMPath))
		if err != nil {
			return errors.Join(ErrCouldNotRemoveVMDir, err)
		}
	}
	return nil
}

func (rp *FirecrackerRuntimeProvider[L, R, G]) Suspend(ctx context.Context, suspendTimeout time.Duration, dg *devicegroup.DeviceGroup) error {
	ctime := time.Now()
	defer func() {
		if rp.Log != nil {
			rp.Log.Info().Int64("ms", time.Since(ctime).Milliseconds()).Msg("Timing Suspend")
		}
	}()

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

	snapshotType := SDKSnapshotTypeMsyncAndState
	if rp.Log != nil {
		if rp.HypervisorConfiguration.NoMapShared {
			// TODO: We don't need the Msync. We just need the state. Change snapshotType when supported in fc
			rp.Log.Debug().Msg("firecracker runtime CreateSnapshot (NoMapShared, so may not need the msync here)")
		} else {
			rp.Log.Debug().Msg("firecracker runtime CreateSnapshot")
		}
	}

	err = rp.Machine.CreateSnapshot(suspendCtx, common.DeviceStateName, "", snapshotType)

	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	// Setup something just incase we get late writes...
	if rp.dg != nil {
		names := rp.dg.GetAllNames()
		for _, devn := range names {
			di := rp.dg.GetDeviceInformationByName(devn)
			hooks := modules.NewHooks(di.Exp.GetProvider())
			hooks.PostWrite = func(buffer []byte, offset int64, n int, err error) (int, error) {
				if rp.Log != nil {
					rp.Log.Error().Str("device", devn).Msg("Write to device after suspend!")
				}
				return n, err
			}
			di.Exp.SetProvider(hooks)
		}
	}

	rp.setRunning(false)

	if rp.GrabMemory {
		err = rp.grabMemoryChanges()
		if err != nil {
			return err
		}
	}

	return nil
}

// grabMemoryChanges
func (rp *FirecrackerRuntimeProvider[L, R, G]) grabMemoryChanges() error {
	rp.memoryLock.Lock()
	defer rp.memoryLock.Unlock()

	ctime := time.Now()
	defer func() {
		if rp.Log != nil {
			rp.Log.Info().Int64("ms", time.Since(ctime).Milliseconds()).Msg("Timing grabMemoryChanges")
		}
	}()
	var err error
	if rp.GrabFailsafe {
		err = rp.grabMemoryChangesFailsafe()
	} else {
		err = rp.grabMemoryChangesSoftDirty()
	}
	if err != nil {
		if rp.Log != nil {
			rp.Log.Error().Err(err).Msg("Grabbing memory changes")
		}
	}
	return err
}

// grabMemoryChangesFailsafe does a memory comparison (bit slow) to get dirty changes.
func (rp *FirecrackerRuntimeProvider[L, R, G]) grabMemoryChangesFailsafe() error {
	if rp.Log != nil {
		rp.Log.Debug().Msg("Grabbing failsafe memory changes")
	}

	pm := memory.NewProcessMemory(rp.Machine.VMPid)
	memRanges, err := pm.GetMemoryRange("/memory")

	if err != nil {
		return err
	}

	var pauseTime time.Time
	var resumeTime time.Time

	lockcb := func() error {
		err := pm.PauseProcess(10 * time.Second)
		pauseTime = time.Now()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not pause process")
			}
		}
		return err
	}

	unlockcb := func() error {
		err := pm.ClearSoftDirty()
		if err != nil {
			return err
		}

		err = pm.ResumeProcess(10 * time.Second)
		resumeTime = time.Now()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not resume process")
			}
			return err
		}
		return nil
	}

	totalBytes := int64(0)

	err = lockcb()
	if err != nil {
		return err
	}

	memf, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", rp.Machine.VMPid), os.O_RDONLY, 0)
	if err != nil {
		return err
	}

	defer memf.Close()

	// Find changes... (SLOW)
	blockSize := uint64(1024 * 1024 * 4)

	memDirty := rp.dg.GetDeviceInformationByName(common.DeviceMemoryName).DirtyRemote

	for _, r := range memRanges {
		buffer := make([]byte, blockSize)
		provBuffer := make([]byte, blockSize)
		for o := r.Start; o < r.End; o += blockSize {
			readSize := blockSize
			if o+readSize > r.End {
				readSize = r.End - o
			}

			n1, err := memf.ReadAt(buffer[:readSize], int64(o))
			if err != nil {
				return err
			}

			n2, err := rp.grabberProv.ReadAt(provBuffer[:readSize], int64(r.Offset+o-r.Start))
			if err != nil {
				return err
			}
			if n1 != n2 {
				return errors.New("Couldn't check memory...\n")
			}
			if !bytes.Equal(buffer[:n1], provBuffer[:n2]) {
				if rp.GrabUpdateDirty {
					memDirty.MarkDirty(int64(r.Offset+o-r.Start), int64(n1))
				}
				if rp.GrabUpdateMemory {
					// Memory has changed, lets write it
					n, err := rp.grabberProv.WriteAt(buffer[:n1], int64(r.Offset+o-r.Start))
					if err != nil {
						return err
					}
					totalBytes += int64(n)
				}
			}
		}
	}

	err = unlockcb()
	if err != nil {
		return err
	}

	if rp.Log != nil {
		rp.Log.Info().Int64("bytes", totalBytes).
			Int64("ms", resumeTime.Sub(pauseTime).Milliseconds()).Msg("failsafe copied memory to the silo provider")
	}

	return nil
}

// grabMemoryChangesSoftDirty grabs memory changes via soft dirty flag etc
func (rp *FirecrackerRuntimeProvider[L, R, G]) grabMemoryChangesSoftDirty() error {
	if rp.Log != nil {
		rp.Log.Debug().Msg("Grabbing softDirty memory changes")
	}

	// Do a softDirty memory read here, and write it to the silo memory device.
	pm := memory.NewProcessMemory(rp.Machine.VMPid)
	memRanges, err := pm.GetMemoryRange("/memory")
	if err != nil {
		return err
	}

	var pauseTime time.Time
	var resumeTime time.Time

	lockcb := func() error {
		err := pm.PauseProcess(10 * time.Second)
		pauseTime = time.Now()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not pause process")
			}
		}
		return err
	}

	unlockcb := func() error {
		err := pm.ClearSoftDirty()
		if err != nil {
			return err
		}

		err = pm.ResumeProcess(10 * time.Second)
		resumeTime = time.Now()
		if err != nil {
			if rp.Log != nil {
				rp.Log.Error().Err(err).Msg("Could not resume process")
			}
			return err
		}
		return nil
	}

	totalBytes := int64(0)

	type CopyData struct {
		ranges    []memory.Range
		addrStart uint64
		offset    uint64
	}

	copyData := make([]CopyData, 0)

	err = lockcb()
	if err != nil {
		return err
	}

	for _, r := range memRanges {
		if rp.Log != nil {
			rp.Log.Debug().Uint64("offset", r.Offset).Uint64("addrEnd", r.End).Uint64("addrStart", r.Start).Msg("SoftDirty memory changes")
		}

		ranges, err := pm.ReadSoftDirtyMemoryRangeList(r.Start, r.End, func() error { return nil }, func() error { return nil }) // lockcb, unlockcb)
		if err != nil {
			_ = unlockcb() // Try our best to unlock...
			return err
		}

		needBytes := uint64(0)
		needSwapBytes := uint64(0)
		for _, r := range ranges {
			needBytes += r.End - r.Start
			needSwapBytes += r.Swapped
		}

		if rp.Log != nil {
			rp.Log.Info().Uint64("bytes", needBytes).Uint64("swapped", needSwapBytes).Msg("Soft dirty memory")
		}

		copyData = append(copyData, CopyData{
			ranges:    ranges,
			addrStart: r.Start,
			offset:    r.Offset,
		})
	}

	err = unlockcb()
	if err != nil {
		return err
	}

	updatedDirtyBytes := uint64(0)
	updatedSyncDirtyBytes := uint64(0)

	// We can update the dirty ranges here.
	if rp.GrabUpdateDirty {
		for _, tt := range copyData {
			for _, r := range tt.ranges {
				if rp.DirectMemoryDirty != nil {
					rp.DirectMemoryDirty.MarkDirty(int64(r.Start-tt.addrStart+tt.offset), int64(r.End-r.Start))
					updatedDirtyBytes += (r.End - r.Start)
				}
				if rp.DirectMemorySyncDirty != nil {
					rp.DirectMemorySyncDirty.MarkDirty(int64(r.Start-tt.addrStart+tt.offset), int64(r.End-r.Start))
					updatedSyncDirtyBytes += (r.End - r.Start)
				}
			}
		}
	}

	if rp.GrabUpdateMemory {
		// Outside the stop/cont, we now copy the regions we need.
		for _, tt := range copyData {
			// Copy to the Silo provider
			b, err := pm.CopyMemoryRanges(int64(tt.addrStart)-int64(tt.offset), tt.ranges, rp.grabberProv)
			if err != nil {
				if rp.Log != nil {
					rp.Log.Error().Err(err).Msg("Could not copy memory ranges")
				}

				return err
			}
			totalBytes += int64(b)
		}

		if rp.Log != nil {
			rp.Log.Info().Int64("bytes", totalBytes).
				Int64("dirtybytes", int64(updatedDirtyBytes)).
				Int64("syncdirtybytes", int64(updatedSyncDirtyBytes)).
				Int64("ms", resumeTime.Sub(pauseTime).Milliseconds()).Msg("SoftDirty copied memory to the silo provider")
		}
	}

	return nil
}
