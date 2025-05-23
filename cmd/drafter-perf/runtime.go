package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
)

type siloConfig struct {
	name                string
	useCow              bool
	useSparseFile       bool
	useVolatility       bool
	useWriteCache       bool
	writeCacheMin       string
	writeCacheMax       string
	writeCacheBlocksize string
	blockSize           uint32
	grabPeriod          time.Duration
}

func (sc *siloConfig) Summary() string {
	s := sc.name
	if sc.useVolatility {
		s = s + " VolatilityMonitor"
	}
	if sc.useCow {
		s = s + " COW"
	}
	if sc.useSparseFile {
		s = s + " SparseFile"
	}
	if sc.useWriteCache {
		s = s + " WriteCache"
	}
	return s
}

/**
 * runSilo runs a benchmark inside a VM with Silo
 *
 */
func runSilo(ctx context.Context, log loggingtypes.Logger, met metrics.SiloMetrics, testDir string, snapDir string, netns string, benchCB func(), conf siloConfig, enableInput bool, enableOutput bool, migrateAfter string, enableInputKeepalive bool) error {
	schemas := make(map[string]*config.DeviceSchema)

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	devicesFrom := make([]common.MigrateFromDevice, 0)
	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			BlockSize:  conf.blockSize,
			Shared:     false,
			SharedBase: true,
			AnyOrder:   !conf.useVolatility,
		}

		if conf.useWriteCache && n == "memory" {
			dev.UseWriteCache = true
			dev.WriteCacheMin = conf.writeCacheMin
			dev.WriteCacheMax = conf.writeCacheMax
			dev.WriteCacheBlocksize = conf.writeCacheBlocksize
		}

		if conf.useCow {
			dev.Base = path.Join(snapDir, n)
			dev.UseSparseFile = conf.useSparseFile
			dev.Overlay = path.Join(path.Join(testDir, conf.name, fmt.Sprintf("%s.overlay", fn)))
			dev.State = path.Join(path.Join(testDir, conf.name, fmt.Sprintf("%s.state", fn)))
		} else {
			// Copy the file
			src, err := os.Open(path.Join(snapDir, n))
			if err != nil {
				return err
			}
			dst, err := os.OpenFile(path.Join(testDir, fmt.Sprintf("silo_%s_%s", conf.name, n)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
			if err != nil {
				return err
			}
			_, err = io.Copy(dst, src)
			if err != nil {
				return err
			}
			err = src.Close()
			if err != nil {
				return err
			}
			err = dst.Close()
			if err != nil {
				return err
			}

			dev.Base = path.Join(testDir, fmt.Sprintf("silo_%s_%s", conf.name, n))
		}
		devicesFrom = append(devicesFrom, dev)

		siloSchema, err := common.CreateSiloDevSchema(&dev)
		if err != nil {
			return err
		}

		schemas[n] = siloSchema
	}

	hConf := rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin:       firecrackerBin,
		JailerBin:            jailerBin,
		ChrootBaseDir:        testDir,
		UID:                  0,
		GID:                  0,
		NetNS:                netns,
		NumaNode:             0,
		CgroupVersion:        2,
		Stdout:               nil,
		Stderr:               nil,
		NoMapShared:          false,
		EnableInputKeepalive: enableInputKeepalive,
	}

	// If we're grabbing memory
	if conf.grabPeriod > 0 {
		hConf.NoMapShared = true
	}

	if enableInput {
		hConf.Stdin = os.Stdin
	}

	if enableOutput {
		hConf.Stdout = os.Stdout
		hConf.Stderr = os.Stderr
	} else {
		fout, err := os.OpenFile(fmt.Sprintf("%s.stdout", conf.name), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0660)
		if err == nil {
			defer fout.Close()
			hConf.Stdout = fout
			hConf.Stderr = fout
		} else {
			fmt.Printf("Could not open output file? %v\n", err)
		}
	}
	rp := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log:                     log,
		HypervisorConfiguration: hConf,
		StateName:               common.DeviceStateName,
		MemoryName:              common.DeviceMemoryName,
		AgentServerLocal:        struct{}{},
		GrabInterval:            conf.grabPeriod,
	}

	if hConf.NoMapShared {
		rp.Grabbing = true
	}

	// Use something to push output (sometimes needed)
	if !enableInput {
		pusherCtx, pusherCancel := context.WithCancel(context.Background())
		r := rfirecracker.NewOutputPusher(pusherCtx, log)
		rp.HypervisorConfiguration.Stdin = r
		rp.RunningCB = func(r bool) {
			if !r {
				pusherCancel()
			}
		}
	}

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, met, nil, conf.name, rp)
	if err != nil {
		return err
	}

	// NB: We set it here to get rid of the uuid prefix Peer adds.
	myPeer.SetInstanceID(conf.name)

	hooks1 := peer.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	err = myPeer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
	if err != nil {
		return err
	}
	/*
		// Hook into the memory so we can see activity
		memExp := myPeer.GetDG().GetDeviceInformationByName(common.DeviceMemoryName).Exp
		hooks := modules.NewHooks(memExp.GetProvider())
		hooks.PostWrite = func(buffer []byte, offset int64, n int, err error) (int, error) {
			for i := 0; i < len(buffer); i += 4096 {
				hash := sha256.Sum256(buffer[i : i+4096])
				fmt.Printf("# VM.WriteAt %016x %x %s\n", offset+int64(i), hash, time.Now())
			}
			return n, err
		}

		hooks.PostRead = func(buffer []byte, offset int64, n int, err error) (int, error) {
			for i := 0; i < len(buffer); i += 4096 {
				hash := sha256.Sum256(buffer[i : i+4096])
				fmt.Printf("# VM.ReadAt %016x %x %s\n", offset+int64(i), hash, time.Now())
			}
			return n, err
		}

		memExp.SetProvider(hooks)
	*/
	err = myPeer.Resume(context.TODO(), 1*time.Minute, 10*time.Second)
	if err != nil {
		return err
	}

	benchCB()

	err = myPeer.Close()
	if err != nil {
		return err
	}

	// Now so we can access the data, lets re-create the Silo devices so they register with the metrics etc
	// NB At the moment, these will never get closed, but since it's just for debug, it's not terrible.
	for name, sc := range schemas {
		sc.Expose = false // We don't need to expose it. Just for internal working.
		_, _, err = device.NewDeviceWithLoggingMetrics(sc, nil, met, fmt.Sprintf("post_%s", conf.name), name)
		if err != nil {
			return err
		}
	}

	return nil
}

/**
 * Run a benchmark with Silo disabled.
 *
 */
func runNonSilo(ctx context.Context, log loggingtypes.Logger, testDir string, snapDir string, netns string, benchCB func(), enableInput bool, enableOutput bool, enableInputKeepalive bool) error {
	// NOW TRY WITHOUT SILO
	agentVsockPort := uint32(26)
	agentLocal := struct{}{}

	deviceFiles := []string{
		"state", "memory", "kernel", "disk", "config", "oci",
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	conf := &rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin:       firecrackerBin,
		JailerBin:            jailerBin,
		ChrootBaseDir:        testDir,
		UID:                  0,
		GID:                  0,
		NetNS:                netns,
		NumaNode:             0,
		CgroupVersion:        2,
		Stdout:               nil,
		Stderr:               nil,
		EnableInputKeepalive: enableInputKeepalive,
	}

	if enableInput {
		conf.Stdin = os.Stdin
	}

	if enableOutput {
		conf.Stdout = os.Stdout
		conf.Stderr = os.Stderr
	} else {
		fout, err := os.OpenFile("nosilo.stdout", os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0660)
		if err == nil {
			defer fout.Close()
			conf.Stdout = fout
			conf.Stderr = fout
		} else {
			fmt.Printf("Could not open output file? %v\n", err)
		}
	}

	m, err := rfirecracker.StartFirecrackerMachine(ctx, log, conf)
	if err != nil {
		return err
	}

	// Copy the devices in from the last place... (No Silo in the mix here)
	for _, d := range deviceFiles {
		src := path.Join(snapDir, d)
		dst := path.Join(m.VMPath, d)
		err = os.Link(src, dst)
		if err != nil {
			return err
		}
	}

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = m.ResumeSnapshot(resumeSnapshotAndAcceptCtx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		return err
	}

	agent, err := ipc.StartAgentRPC[struct{}, ipc.AgentServerRemote[struct{}]](
		log, path.Join(m.VMPath, rfirecracker.VSockName),
		agentVsockPort, agentLocal)
	if err != nil {
		return err
	}

	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelAfterResumeCtx()

	r, err := agent.GetRemote(afterResumeCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		return err
	}

	benchCB()

	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSuspendCtx()

	r, err = agent.GetRemote(suspendCtx)
	if err != nil {
		return err
	}

	remote = *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	if err != nil {
		return err
	}

	err = agent.Close()
	if err != nil {
		return err
	}

	return nil
}
