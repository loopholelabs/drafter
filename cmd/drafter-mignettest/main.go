package main

import (
	"context"
	"errors"
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
)

var cpuTemplate = "None"
var bootArgs = rfirecracker.DefaultBootArgsNoPVM

// Configuration
var blueprintsDir *string
var snapshotsDir *string
var networkNamespace *string

var destinationAddr *string
var listenAddr *string

var start *bool
var waitForComplete *bool
var testDirectory *string
var usePVM *bool
var sleepTime *time.Duration
var iterations *int

var s3sync *bool
var s3endpoint *string
var s3bucket *string
var s3accesskey *string
var s3secretkey *string

// main()
func main() {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//log.SetLevel(types.DebugLevel)

	defer func() {
		//		os.RemoveAll(createSnapshotDir)
		//		os.RemoveAll(testPeerDirCowS3)
	}()
	iterations = flag.Int("num", 10, "number of iterations")
	sleepTime = flag.Duration("sleep", 5*time.Second, "sleep inbetween resume/suspend")
	blueprintsDir = flag.String("blueprints", "", "blueprints dir")
	snapshotsDir = flag.String("snapshot", "", "snapshot dir")
	start = flag.Bool("start", false, "start here")
	testDirectory = flag.String("dir", "test", "test dir")
	networkNamespace = flag.String("netns", "ark0", "Network namespace")
	destinationAddr = flag.String("dest", "", "destination address")
	listenAddr = flag.String("listen", "", "listen address")
	waitForComplete = flag.Bool("wait", true, "wait for complete")
	usePVM = flag.Bool("pvm", false, "use PVM boot args and T2A CPU template")

	s3sync = flag.Bool("s3sync", false, "Use S3 to speed up migrations")
	s3endpoint = flag.String("s3endpoint", "", "S3 endpoint")
	s3bucket = flag.String("s3bucket", "", "S3 bucket")
	s3accesskey = flag.String("s3accesskey", "", "S3 access key")
	s3secretkey = flag.String("s3secretkey", "", "S3 secret key")

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
	if *blueprintsDir != "" {
		err := setupSnapshot(log, context.Background(), *snapshotsDir, *blueprintsDir)
		if err != nil {
			panic(err)
		}
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

	err = os.Mkdir(*testDirectory, 0777)
	if err != nil {
		panic(err)
	}

	if *start {
		myPeer, err := setupFirstPeer(log, firecrackerBin, jailerBin, *snapshotsDir)
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
		opts := &common.MigrateToOptions{
			Concurrency: 10,
			Compression: true,
		}
		err = myPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, opts, []io.Reader{conn}, []io.Writer{conn}, hooks)
		if err != nil {
			panic(err)
		}

		// Close the connection from here...
		err = conn.Close()
		if err != nil {
			panic(err)
		}

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

		err = handleConnection(migration, conn, log, firecrackerBin, jailerBin, devicesTo)
		if err != nil {
			log.Error().Err(err).Msg("Error handling connection")
			return
		}
		migration++

		if migration == *iterations {
			fmt.Printf("ALL DONE\n")
			return
		}
	}

}

// handleConnection
func handleConnection(migration int, conn net.Conn, log types.Logger, firecrackerBin string, jailerBin string, devicesTo []common.MigrateToDevice) error {
	// Receive an incoming VM, run it for a bit, then send it on...

	// Create a new RuntimeProvider
	rp2 := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log: log,
		HypervisorConfiguration: rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  *testDirectory,
			UID:            0,
			GID:            0,
			NetNS:          *networkNamespace,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         os.Stdout,
			Stderr:         os.Stderr,
			Stdin:          nil,
		},
		StateName:        common.DeviceStateName,
		MemoryName:       common.DeviceMemoryName,
		AgentServerLocal: struct{}{},
	}

	// Use something to push output (sometimes needed)
	pusherCtx, pusherCancel := context.WithCancel(context.Background())
	r := rfirecracker.NewOutputPusher(pusherCtx, log)
	rp2.HypervisorConfiguration.Stdin = r
	rp2.RunningCB = func(r bool) {
		if !r {
			pusherCancel()
		}
	}

	nextPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp2)
	if err != nil {
		return err
	}

	var completedWg sync.WaitGroup
	completedWg.Add(1)
	hooks2 := peer.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
		OnCompletion: func() {
			log.Info().Msg("migration completed")
			completedWg.Done()
		},
	}
	devicesFrom := getDevicesFrom(*snapshotsDir, migration+1)
	err = nextPeer.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{conn}, []io.Writer{conn}, hooks2)
	if err != nil {
		return err
	}

	if *waitForComplete {
		completedWg.Wait()

		// We can resume here safely
		err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		if err != nil {
			return err
		}
	} else {
		// We can resume here safely
		err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		if err != nil {
			return err
		}
		completedWg.Wait()
	}

	// Resumed. Now sleep for a bit, and send it on.

	time.Sleep(*sleepTime)

	toConn, err := net.Dial("tcp", *destinationAddr)
	if err != nil {
		// Could not connect, assume the other end has gone, so lets shutdown
		closeErr := nextPeer.Close()
		return errors.Join(err, closeErr)
	}

	hooks := peer.MigrateToHooks{
		OnBeforeSuspend:          func() {},
		OnAfterSuspend:           func() {},
		OnAllMigrationsCompleted: func() {},
		OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
		GetXferCustomData:        func() []byte { return []byte{} },
	}

	ctime := time.Now()
	opts := &common.MigrateToOptions{
		Concurrency: 10,
		Compression: true,
	}
	err = nextPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, opts, []io.Reader{toConn}, []io.Writer{toConn}, hooks)
	if err != nil {
		return err
	}

	// Close the connection from here...
	err = toConn.Close()
	if err != nil {
		return err
	}

	err = nextPeer.Close()
	if err != nil {
		return err
	}

	fmt.Printf("MIGRATION %d TO completed in %dms\n", migration, time.Since(ctime).Milliseconds())
	return nil
}

// setupFirstPeer starts the first peer from a snapshot dir.
func setupFirstPeer(log types.Logger, firecrackerBin string, jailerBin string, snapDir string) (*peer.Peer, error) {
	rp := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log: log,
		HypervisorConfiguration: rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  *testDirectory,
			UID:            0,
			GID:            0,
			NetNS:          *networkNamespace,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         os.Stdout,
			Stderr:         os.Stderr,
			Stdin:          nil,
		},
		StateName:        common.DeviceStateName,
		MemoryName:       common.DeviceMemoryName,
		AgentServerLocal: struct{}{},
	}

	// Use something to push output (sometimes needed)
	pusherCtx, pusherCancel := context.WithCancel(context.Background())
	r := rfirecracker.NewOutputPusher(pusherCtx, log)
	rp.HypervisorConfiguration.Stdin = r
	rp.RunningCB = func(r bool) {
		if !r {
			pusherCancel()
		}
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

	devicesFrom := getDevicesFrom(snapDir, 0)
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

func getDevicesFrom(snapDir string, i int) []common.MigrateFromDevice {
	devicesFrom := make([]common.MigrateFromDevice, 0)

	err := os.Mkdir(path.Join(*testDirectory, fmt.Sprintf("migration_%d", i)), 0777)
	if err != nil {
		panic(err)
	}

	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			Base:       path.Join(snapDir, n),
			Overlay:    path.Join(*testDirectory, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.overlay", fn)),
			State:      path.Join(*testDirectory, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.state", fn)),
			BlockSize:  1024 * 1024,
			Shared:     false,
			SharedBase: true,
		}

		if *s3sync && (n == common.DeviceMemoryName || n == common.DeviceDiskName) {
			dev.S3Sync = true
			dev.S3AccessKey = *s3accesskey
			dev.S3SecretKey = *s3secretkey
			dev.S3Endpoint = *s3endpoint
			dev.S3Secure = true
			dev.S3Bucket = *s3bucket
			dev.S3Concurrency = 10

			dev.S3BlockShift = 2
			dev.S3OnlyDirty = false
			dev.S3MaxAge = "100ms"
			dev.S3MinChanged = 4
			dev.S3Limit = 256
			dev.S3CheckPeriod = "100ms"
		}
		devicesFrom = append(devicesFrom, dev)
	}
	return devicesFrom
}
