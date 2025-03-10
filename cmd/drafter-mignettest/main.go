package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
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
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/testutils"
)

const createSnapshotDir = "snap_test"

var cpuTemplate = "None"
var bootArgs = rfirecracker.DefaultBootArgsNoPVM

var blueprintsDir *string
var snapshotsDir *string

var networkNamespace *string

var destinationAddr *string
var listenAddr *string
var start *bool
var waitForComplete *bool

var testPeerDirCowS3 *string

var usePVM *bool

const enableS3 = false
const performHashChecks = false

func main() {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//log.SetLevel(types.DebugLevel)

	defer func() {
		//		os.RemoveAll(createSnapshotDir)
		//		os.RemoveAll(testPeerDirCowS3)
	}()
	iterations := flag.Int("num", 10, "number of iterations")
	sleepTime := flag.Duration("sleep", 5*time.Second, "sleep inbetween resume/suspend")
	blueprintsDir = flag.String("blueprints", "", "blueprints dir")
	snapshotsDir = flag.String("snapshot", "", "snapshot dir")
	start = flag.Bool("start", false, "start here")
	testPeerDirCowS3 = flag.String("dir", "test", "test dir")
	networkNamespace = flag.String("netns", "ark0", "Network namespace")
	destinationAddr = flag.String("dest", "", "destination address")
	listenAddr = flag.String("listen", "", "listen address")
	waitForComplete = flag.Bool("wait", true, "wait for complete")
	usePVM = flag.Bool("pvm", false, "use PVM boot args and T2A CPU template")

	flag.Parse()

	if *usePVM {
		cpuTemplate = "T2A"
		bootArgs = rfirecracker.DefaultBootArgs
	}

	devicesTo := make([]common.MigrateToDevice, 0)
	for _, n := range append(common.KnownNames, "oci") {
		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: 10,
			MinCycles:      1,
			MaxCycles:      1,
			CycleThrottle:  100 * time.Millisecond,
		})

	}

	// create package files
	snapDir := *snapshotsDir
	if *blueprintsDir != "" {
		snapDir = setupSnapshot(log, context.Background())
	}

	s3Endpoint := setupDevicesCowS3(log)

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

	err = os.Mkdir(*testPeerDirCowS3, 0777)
	if err != nil {
		panic(err)
	}

	if *start {
		myPeer, err := setupFirstPeer(log, firecrackerBin, jailerBin, snapDir, s3Endpoint)
		if err != nil {
			panic(err)
		}
		// Now we have a FIRST "resumed peer"
		// Send the VM to destination

		conn, err := net.Dial("tcp", *destinationAddr)
		if err != nil {
			// Maybe they just wanted a snapshot!
			err = myPeer.Close()
			if err != nil {
				panic(err)
			}
			fmt.Printf("Can't connect, so shutting down. Enjoy the snapshot\n")
			return
		}

		hooks := peer.MigrateToHooks{
			OnBeforeSuspend:          func() {},
			OnAfterSuspend:           func() {},
			OnAllMigrationsCompleted: func() {},
			OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
			GetXferCustomData:        func() []byte { return []byte{} },
		}

		ctime := time.Now()
		err = myPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, 10, []io.Reader{conn}, []io.Writer{conn}, hooks)
		if err != nil {
			panic(err)
		}

		// Close the connection from here...
		conn.Close()

		err = myPeer.Close()
		if err != nil {
			panic(err)
		}

		fmt.Printf("MIGRATION TO completed in %dms\n", time.Since(ctime).Milliseconds())
	}

	// Now just accept connections...

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		panic(err)
	}

	migration := 0

	for {
		conn, err := listener.Accept()
		if err != nil {
			panic(err)
		}

		// Receive an incoming VM, run it for a bit, then send it on...

		// Create a new RuntimeProvider
		rp2 := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Log: log,
			HypervisorConfiguration: rfirecracker.HypervisorConfiguration{
				FirecrackerBin: firecrackerBin,
				JailerBin:      jailerBin,
				ChrootBaseDir:  *testPeerDirCowS3,
				UID:            0,
				GID:            0,
				NetNS:          *networkNamespace,
				NumaNode:       0,
				CgroupVersion:  2,
				EnableOutput:   true,
				EnableInput:    false,
			},
			StateName:        common.DeviceStateName,
			MemoryName:       common.DeviceMemoryName,
			AgentServerLocal: struct{}{},
			AgentServerHooks: ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{},
			SnapshotLoadConfiguration: rfirecracker.SnapshotLoadConfiguration{
				ExperimentalMapPrivate: false,
			},
		}

		nextPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp2)
		if err != nil {
			panic(err)
		}

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
		devicesFrom := getDevicesFrom(snapDir, s3Endpoint, migration+1)
		err = nextPeer.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{conn}, []io.Writer{conn}, hooks2)

		if *waitForComplete {
			completedWg.Wait()

			// We can resume here safely
			err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
			if err != nil {
				panic(err)
			}
		} else {
			// We can resume here safely
			err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
			if err != nil {
				panic(err)
			}
			completedWg.Wait()

		}

		// Resumed. Now sleep for a bit, and send it on.

		time.Sleep(*sleepTime)

		toConn, err := net.Dial("tcp", *destinationAddr)
		if err != nil {
			// Could not connect, assume the other end has gone, so lets shutdown
			fmt.Printf("Connect error %v\n", err)
			err = nextPeer.Close()
			if err != nil {
				panic(err)
			}
			return
		}

		hooks := peer.MigrateToHooks{
			OnBeforeSuspend:          func() {},
			OnAfterSuspend:           func() {},
			OnAllMigrationsCompleted: func() {},
			OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
			GetXferCustomData:        func() []byte { return []byte{} },
		}

		ctime := time.Now()
		err = nextPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, 10, []io.Reader{toConn}, []io.Writer{toConn}, hooks)
		if err != nil {
			panic(err)
		}

		// Close the connection from here...
		toConn.Close()

		err = nextPeer.Close()
		if err != nil {
			panic(err)
		}

		fmt.Printf("MIGRATION %d TO completed in %dms\n", migration, time.Since(ctime).Milliseconds())

		migration++

		if migration == *iterations {
			fmt.Printf("ALL DONE\n")
			return
		}
	}

}

func setupFirstPeer(log types.Logger, firecrackerBin string, jailerBin string, snapDir string, s3Endpoint string) (*peer.Peer, error) {
	rp := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log: log,
		HypervisorConfiguration: rfirecracker.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  *testPeerDirCowS3,
			UID:            0,
			GID:            0,
			NetNS:          *networkNamespace,
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   true,
			EnableInput:    false,
		},
		StateName:        common.DeviceStateName,
		MemoryName:       common.DeviceMemoryName,
		AgentServerLocal: struct{}{},
		AgentServerHooks: ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{},
		SnapshotLoadConfiguration: rfirecracker.SnapshotLoadConfiguration{
			ExperimentalMapPrivate: false,
		},
	}

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	err = myPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	if err != nil {
		return nil, err
	}

	// Now we have a FIRST "resumed peer"
	return myPeer, nil
}

func setupDevicesCowS3(log types.Logger) string {
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

	return s3Endpoint
}

func getDevicesFrom(snapDir string, s3Endpoint string, i int) []common.MigrateFromDevice {
	devicesFrom := make([]common.MigrateFromDevice, 0)

	err := os.Mkdir(path.Join(*testPeerDirCowS3, fmt.Sprintf("migration_%d", i)), 0777)
	if err != nil {
		panic(err)
	}

	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			Base:       path.Join(snapDir, n),
			Overlay:    path.Join(*testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.overlay", fn)),
			State:      path.Join(*testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.state", fn)),
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

	err = rfirecracker.CreateSnapshot(log, ctx, devices,
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
		rfirecracker.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  createSnapshotDir,
			UID:            0,
			GID:            0,
			NetNS:          *networkNamespace,
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   true,
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
	)

	if err != nil {
		panic(err)
	}

	return createSnapshotDir
}
