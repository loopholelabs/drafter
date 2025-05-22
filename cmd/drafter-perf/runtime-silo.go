package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
)

type RunConfig struct {
	Name                string        `json:"name"`
	UseCow              bool          `json:"cow"`
	UseSparseFile       bool          `json:"sparse"`
	UseVolatility       bool          `json:"volatility"`
	UseWriteCache       bool          `json:"writecache"`
	WriteCacheMin       string        `json:"writecachemin"`
	WriteCacheMax       string        `json:"writecachemax"`
	WriteCacheBlocksize string        `json:"writecacheblocksize"`
	BlockSize           uint32        `json:"blocksize"`
	GrabPeriod          time.Duration `json:"grabperiod"`
	NoMapShared         bool          `json:"nomapshared"`
}

func (sc *RunConfig) Summary() string {
	s := sc.Name
	if sc.UseVolatility {
		s = s + " VolatilityMonitor"
	}
	if sc.UseCow {
		s = s + " COW"
	}
	if sc.UseSparseFile {
		s = s + " SparseFile"
	}
	if sc.UseWriteCache {
		s = s + " WriteCache"
	}
	if sc.NoMapShared {
		s = s + " NoMapShared"
	}
	s = fmt.Sprintf("%s bs=%d grab=%s", s, sc.BlockSize, sc.GrabPeriod)
	return s
}

/**
 * runSilo runs a benchmark inside a VM with Silo
 *
 */
func runSilo(ctx context.Context, log loggingtypes.Logger, met metrics.SiloMetrics, testDir string, snapDir string, netns string, benchCB func(), conf RunConfig, enableInput bool, enableOutput bool, migrateAfter string) error {
	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	schemas, devicesFrom, err := getDevicesFrom(0, testDir, snapDir, conf)
	if err != nil {
		return err
	}

	hConf := rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  testDir,
		UID:            0,
		GID:            0,
		NetNS:          netns,
		NumaNode:       0,
		CgroupVersion:  2,
		Stdout:         nil,
		Stderr:         nil,
		NoMapShared:    conf.NoMapShared,
	}

	if enableInput {
		hConf.Stdin = os.Stdin
	}

	if enableOutput {
		hConf.Stdout = os.Stdout
		hConf.Stderr = os.Stderr
	} else {
		fout, err := os.OpenFile(fmt.Sprintf("%s.stdout", conf.Name), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0660)
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
		GrabInterval:            conf.GrabPeriod,
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

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, met, nil, conf.Name, rp)
	if err != nil {
		return err
	}

	// NB: We set it here to get rid of the uuid prefix Peer adds.
	myPeer.SetInstanceID(conf.Name)

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
		_, _, err = device.NewDeviceWithLoggingMetrics(sc, nil, met, fmt.Sprintf("post_%s", conf.Name), name)
		if err != nil {
			return err
		}
	}

	return nil
}

// getDevicesFrom configures the silo devices
func getDevicesFrom(id int, snapDir string, testDir string, conf RunConfig) (map[string]*config.DeviceSchema, []common.MigrateFromDevice, error) {
	schemas := make(map[string]*config.DeviceSchema)
	devicesFrom := make([]common.MigrateFromDevice, 0)
	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			BlockSize:  conf.BlockSize,
			Shared:     false,
			SharedBase: true,
			AnyOrder:   !conf.UseVolatility,
		}

		if conf.UseWriteCache && n == "memory" {
			dev.UseWriteCache = true
			dev.WriteCacheMin = conf.WriteCacheMin
			dev.WriteCacheMax = conf.WriteCacheMax
			dev.WriteCacheBlocksize = conf.WriteCacheBlocksize
		}

		if conf.UseCow {
			dev.Base = path.Join(snapDir, n)
			dev.UseSparseFile = conf.UseSparseFile
			dev.Overlay = path.Join(path.Join(testDir, conf.Name, fmt.Sprintf("%s-%d.overlay", fn, id)))
			dev.State = path.Join(path.Join(testDir, conf.Name, fmt.Sprintf("%s-%d.state", fn, id)))
		} else {
			dev.Base = path.Join(testDir, fmt.Sprintf("silo_%s_%d_%s", conf.Name, id, n))
			// Copy the file
			src, err := os.Open(path.Join(snapDir, n))
			if err != nil {
				return nil, nil, err
			}
			dst, err := os.OpenFile(dev.Base, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
			if err != nil {
				return nil, nil, err
			}
			_, err = io.Copy(dst, src)
			if err != nil {
				return nil, nil, err
			}
			err = src.Close()
			if err != nil {
				return nil, nil, err
			}
			err = dst.Close()
			if err != nil {
				return nil, nil, err
			}
		}
		devicesFrom = append(devicesFrom, dev)

		siloSchema, err := common.CreateSiloDevSchema(&dev)
		if err != nil {
			return nil, nil, err
		}

		schemas[n] = siloSchema
	}
	return schemas, devicesFrom, nil
}
