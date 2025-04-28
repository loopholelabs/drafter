package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
	siloprom "github.com/loopholelabs/silo/pkg/storage/metrics/prometheus"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/testutils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const createSnapshotDir = "snap_test"

const cpuTemplate = "None"
const bootArgs = rfirecracker.DefaultBootArgsNoPVM

var blueprintsDir *string
var snapshotsDir *string

const testPeerDirCowS3 = "test_peer_cow"
const enableS3 = false
const performHashChecks = false

var serveMetrics *string

func main() {
	defer func() {
		os.RemoveAll(createSnapshotDir)
		os.RemoveAll(testPeerDirCowS3)
	}()
	iterations := flag.Int("num", 10, "number of iterations")
	sleepTime := flag.Duration("sleep", 5*time.Second, "sleep inbetween resume/suspend")
	blueprintsDir = flag.String("blueprints", "blueprints", "blueprints dir")
	snapshotsDir = flag.String("snapshot", "", "snapshot dir")
	serveMetrics = flag.String("metrics", "", "metrics")

	flag.Parse()

	var siloMetrics metrics.SiloMetrics
	var drafterMetrics *common.DrafterMetrics
	var reg *prometheus.Registry
	if *serveMetrics != "" {
		reg = prometheus.NewRegistry()

		// Add the default go metrics
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)

		siloMetrics = siloprom.New(reg, siloprom.DefaultConfig())
		drafterMetrics = common.NewDrafterMetrics(reg)

		http.Handle("/metrics", promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{
				// Opt into OpenMetrics to support exemplars.
				EnableOpenMetrics: true,
				// Pass custom registry
				Registry: reg,
			},
		))

		go http.ListenAndServe(*serveMetrics, nil)
	}

	err := os.Mkdir(testPeerDirCowS3, 0777)
	if err != nil {
		panic(err)
	}

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//log.SetLevel(types.DebugLevel)

	grandTotalBlocksP2P := 0
	grandTotalBlocksS3 := 0

	devicesTo, snapDir, s3Endpoint := setupDevicesCowS3(log, *snapshotsDir)

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

	fcconfig := rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  testPeerDirCowS3,
		UID:            0,
		GID:            0,
		NetNS:          "ark0",
		NumaNode:       0,
		CgroupVersion:  2,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		EnableInput:    false,
	}

	rp := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log:                     log,
		HypervisorConfiguration: fcconfig,
		StateName:               common.DeviceStateName,
		MemoryName:              common.DeviceMemoryName,
		AgentServerLocal:        struct{}{},
	}

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, siloMetrics, drafterMetrics, "cow_test", rp)
	if err != nil {
		panic(err)
	}

	hooks1 := peer.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	devicesFrom := getDevicesFrom(snapDir, s3Endpoint, 0)
	err = myPeer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
	if err != nil {
		panic(err)
	}

	err = myPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	if err != nil {
		panic(err)
	}

	// Now we have a FIRST "resumed peer"

	// Lets send it on a migration journey...

	var lastPeer = myPeer

	for migration := 0; migration < *iterations; migration++ {

		// Let some S3 sync go on...
		time.Sleep(*sleepTime)

		// Create a new RuntimeProvider
		rp2 := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Log: log,
			HypervisorConfiguration: rfirecracker.FirecrackerMachineConfig{
				FirecrackerBin: firecrackerBin,
				JailerBin:      jailerBin,
				ChrootBaseDir:  testPeerDirCowS3,
				UID:            0,
				GID:            0,
				NetNS:          "ark0",
				NumaNode:       0,
				CgroupVersion:  2,
				Stdout:         os.Stdout,
				Stderr:         os.Stderr,
				EnableInput:    false,
			},
			StateName:        common.DeviceStateName,
			MemoryName:       common.DeviceMemoryName,
			AgentServerLocal: struct{}{},
		}

		nextPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, siloMetrics, drafterMetrics, "cow_test", rp2)
		if err != nil {
			panic(err)
		}

		r1, w1 := io.Pipe()
		r2, w2 := io.Pipe()

		hooks := peer.MigrateToHooks{
			OnBeforeSuspend:          func() {},
			OnAfterSuspend:           func() {},
			OnAllMigrationsCompleted: func() {},
			OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
			GetXferCustomData:        func() []byte { return []byte{} },
		}

		migrateStartTime := time.Now()

		var wg sync.WaitGroup
		var sendingErr error
		wg.Add(1)
		go func() {
			err := lastPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, 10, []io.Reader{r1}, []io.Writer{w2}, hooks)
			if err != nil {
				panic(err)
			}
			sendingErr = err

			// Close the connection from here...
			// Close from the sending side
			r1.Close()
			w2.Close()

			wg.Done()
		}()

		var completedWg sync.WaitGroup
		completedWg.Add(1)
		hooks2 := peer.MigrateFromHooks{
			OnLocalDeviceRequested:     func(id uint32, path string) {},
			OnLocalDeviceExposed:       func(id uint32, path string) {},
			OnLocalAllDevicesRequested: func() {},
			OnXferCustomData:           func(data []byte) {},
			OnCompletion: func() {
				fmt.Printf("Completed!\n")
				completedWg.Done()
			},
		}
		devicesFrom = getDevicesFrom(snapDir, s3Endpoint, migration+1)
		err = nextPeer.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{r2}, []io.Writer{w1}, hooks2)
		if err != nil {
			panic(err)
		}

		// Wait for sending side to complete
		wg.Wait()

		evacuationTook := time.Since(migrateStartTime)

		// Make sure we got the completion callback
		completedWg.Wait()

		migrationTook := time.Since(migrateStartTime)

		// Tot up some data...
		totalBlocksP2P := 0
		totalBlocksS3 := 0
		for _, n := range nextPeer.GetDG().GetAllNames() {
			di := nextPeer.GetDG().GetDeviceInformationByName(n)
			me := di.From.GetMetrics()
			totalBlocksP2P += len(me.AvailableP2P)
			totalBlocksS3 += len(me.AvailableAltSources)
			/*
				wc := di.WaitingCacheLocal.GetMetrics()
				fmt.Printf("From Migrated %s (%d P2P) (%d S3) / %d\n", n, len(me.AvailableP2P), len(me.AvailableAltSources), di.NumBlocks)
				fmt.Printf("WC available local %d, remote %d\n", wc.AvailableLocal, wc.AvailableRemote)

				fmt.Printf("WritesAllowed (P2P %d S3 %d)\n", me.WritesAllowedP2P, me.WritesAllowedAltSources)
				fmt.Printf("WritesBlocked (P2P %d S3 %d)\n", me.WritesBlockedP2P, me.WritesBlockedAltSources)
			*/
		}

		if performHashChecks && err == nil && sendingErr == nil {
			// Make sure everything migrated as expected...
			for _, n := range common.KnownNames {

				buff1, err := os.ReadFile(path.Join(rp.DevicePath(), n))
				if err != nil {
					panic(err)
				}
				buff2, err := os.ReadFile(path.Join(rp2.DevicePath(), n))
				if err != nil {
					panic(err)
				}

				// Compare hashes so we don't get tons of output if they do differ.
				hash1 := sha256.Sum256(buff1)
				hash2 := sha256.Sum256(buff2)

				fmt.Printf(" # Migration %d End hash %s ~ %x => %x\n", migration+1, n, hash1, hash2)

				// Check the data is identical
				if err != nil {
					panic(err)
				}
			}
		}

		grandTotalBlocksP2P += totalBlocksP2P
		grandTotalBlocksS3 += totalBlocksS3

		// Close the last peer
		err = lastPeer.Close()
		if err != nil {
			panic(err)
		}

		// We can resume here safely
		err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		if err != nil {
			panic(err)
		}

		lastPeer = nextPeer

		fmt.Printf("\nMIGRATION %d COMPLETED evacuated in %dms migrated in %dms (%d P2P) (%d S3)\n\n", migration+1,
			evacuationTook.Milliseconds(), migrationTook.Milliseconds(), totalBlocksP2P, totalBlocksS3)
	}

	// Close the final peer
	err = lastPeer.Close()
	if err != nil {
		panic(err)
	}
}

func setupDevicesCowS3(log types.Logger, snapDir string) ([]common.MigrateToDevice, string, string) {
	s3Endpoint := ""

	if enableS3 {
		cleanup := func(func()) {}

		s3port := testutils.SetupMinioWithExpiry(cleanup, 120*time.Minute)
		s3Endpoint = fmt.Sprintf("localhost:%s", s3port)

		err := sources.CreateBucket(false, s3Endpoint, "silosilo", "silosilo", "silosilo")
		if err != nil {
			panic(err)
		}
	}

	devicesTo := make([]common.MigrateToDevice, 0)

	// create package files
	if snapDir == "" {
		snapDir = setupSnapshot(log, context.Background())
	}

	for _, n := range append(common.KnownNames, "oci") {
		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: 10,
			MinCycles:      1,
			MaxCycles:      1,
			CycleThrottle:  100 * time.Millisecond,
		})

	}
	return devicesTo, snapDir, s3Endpoint
}

func getDevicesFrom(snapDir string, s3Endpoint string, i int) []common.MigrateFromDevice {
	devicesFrom := make([]common.MigrateFromDevice, 0)

	err := os.Mkdir(path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i)), 0777)
	if err != nil {
		panic(err)
	}

	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			Base:       path.Join(snapDir, n),
			Overlay:    path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.overlay", fn)),
			State:      path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.state", fn)),
			BlockSize:  1024 * 1024,
			Shared:     false,
			SharedBase: true,
		}

		if enableS3 && (n == common.DeviceMemoryName || n == common.DeviceDiskName) {
			dev.S3Sync = true
			dev.S3AccessKey = "silosilo"
			dev.S3SecretKey = "silosilo"
			dev.S3Endpoint = s3Endpoint
			dev.S3Secure = false
			dev.S3Bucket = "silosilo"
			dev.S3Concurrency = 10
		}
		devicesFrom = append(devicesFrom, dev)
	}
	return devicesFrom
}

/**
 * Pre-requisites
 *  - ark0 network namespace exists
 *  - firecracker works
 *  - blueprints exist
 */
func setupSnapshot(log types.Logger, ctx context.Context) string {
	err := os.Mkdir(createSnapshotDir, 0777)
	if err != nil {
		panic(err)
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

	devices := []rfirecracker.SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(createSnapshotDir, "state"),
		},
		{
			Name:   "memory",
			Output: path.Join(createSnapshotDir, "memory"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(*blueprintsDir, "vmlinux"),
			Output: path.Join(createSnapshotDir, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(*blueprintsDir, "rootfs.ext4"),
			Output: path.Join(createSnapshotDir, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(createSnapshotDir, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(*blueprintsDir, "oci.ext4"),
			Output: path.Join(createSnapshotDir, "oci"),
		},
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, true,
		rfirecracker.VMConfiguration{
			CPUCount:    1,
			MemorySize:  1024,
			CPUTemplate: cpuTemplate,
			BootArgs:    bootArgs,
		},
		rfirecracker.LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  createSnapshotDir,
			UID:            0,
			GID:            0,
			NetNS:          "ark0",
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         nil,
			Stderr:         nil,
			EnableInput:    false,
		},
		rfirecracker.NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		rfirecracker.AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
		func() {})

	if err != nil {
		panic(err)
	}

	return createSnapshotDir
}
