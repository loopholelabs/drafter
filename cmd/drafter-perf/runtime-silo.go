package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/drafter/pkg/testutil"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/memory"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/muesli/gotable"
)

type RunConfig struct {
	Name                string        `json:"name"`
	UseCow              bool          `json:"cow"`
	UseSharedBase       bool          `json:"sharedbase"`
	UseSparseFile       bool          `json:"sparse"`
	UseVolatility       bool          `json:"volatility"`
	UseWriteCache       bool          `json:"writecache"`
	WriteCacheMin       string        `json:"writecachemin"`
	WriteCacheMax       string        `json:"writecachemax"`
	WriteCacheBlocksize string        `json:"writecacheblocksize"`
	BlockSize           uint32        `json:"blocksize"`
	GrabPeriod          time.Duration `json:"grabperiod"`
	NoMapShared         bool          `json:"nomapshared"`
	GrabFailsafe        bool          `json:"grabfailsafe"`
	DirectMemory        bool          `json:"directmemory"`

	S3Sync        bool   `json:"s3sync"`
	S3Secure      bool   `json:"s3secure"`
	S3SecretKey   string `json:"s3secretkey"`
	S3Endpoint    string `json:"s3endpoint"`
	S3Concurrency int    `json:"s3concurrency"`
	S3Bucket      string `json:"s3bucket"`
	S3AccessKey   string `json:"s3accesskey"`

	S3BlockShift  int    `json:"s3blockshift"`
	S3OnlyDirty   bool   `json:"s3onlydirty"`
	S3MaxAge      string `json:"s3maxage"`
	S3MinChanged  int    `json:"s3minchanged"`
	S3Limit       int    `json:"s3limit"`
	S3CheckPeriod string `json:"s3checkperiod"`

	MigrateAfter    string `json:"migrateafter"`
	MigrateInterval string `json:"migrateinterval"`

	MigrationCompression bool `json:"migrationcompression"`
	MigrationConcurrency int  `json:"migrationconcurrency"`

	NumPipes int `json:"numpipes"`

	// TODO
	VMCPUs   int `json:"cpus"`
	VMMemory int `json:"memory"`
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
func runSilo(ctx context.Context, log loggingtypes.Logger, met *testutil.DummyMetrics,
	testDir string, snapDir string, ns func() (string, func(), error), forwards func(string) (func(), error), benchCB func(), conf RunConfig,
	enableInput bool, enableOutput bool) error {

	peers := make([]*peer.Peer, 0)

	// Setup the first devices here...
	_, devicesFrom, err := getDevicesFrom(0, testDir, snapDir, conf)
	if err != nil {
		return err
	}

	myNetNs, myNetCloser, err := ns()
	if err != nil {
		return err
	}
	myForwardsCloser, err := forwards(myNetNs)
	if err != nil {
		return err
	}

	defer func() {
		myForwardsCloser() // Might have already been done
		myNetCloser()      // Close the net
	}()

	myPeer, err := setupPeer(log, met, conf, testDir, myNetNs, enableInput, enableOutput, 0, func(_ bool) {})
	if err != nil {
		return err
	}

	defer func() {
		fmt.Printf("VMClosing %s\n", myPeer.VMPath)
		err = myPeer.Close()
		if err != nil {
			fmt.Printf("Error closing VM %v\n", err)
		}
	}()

	peers = append(peers, myPeer)

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

	err = myPeer.Resume(context.TODO(), 5*time.Minute, 5*time.Minute)
	if err != nil {
		return err
	}

	doneBench := make(chan bool)

	go func() {
		benchCB()
		close(doneBench)
	}()

	// Our main loop here.

	afterChan := make(<-chan time.Time)
	if conf.MigrateAfter != "" {
		afterDuration, err := time.ParseDuration(conf.MigrateAfter)
		if err != nil {
			return err
		}
		afterChan = time.After(afterDuration)
	}
	if conf.MigrateInterval != "" {
		intervalDuration, err := time.ParseDuration(conf.MigrateInterval)
		if err != nil {
			return err
		}
		afterChan = time.NewTicker(intervalDuration).C
	}

	migrationID := 1

mainloop:
	for {
		select {
		case <-doneBench:
			break mainloop
		case <-afterChan:
			// Do a migration here

			newPeer, err := setupPeer(log, met, conf, testDir, myNetNs, enableInput, enableOutput, migrationID, func(_ bool) {})
			if err != nil {
				return err
			}

			peers = append(peers, newPeer)

			migStartTime := time.Now()
			err = migrateNow(migrationID, log, met, conf, myPeer, newPeer, testDir, snapDir)
			if err != nil {
				return err
			}

			fmt.Printf("# Migration.%d took %dms\n", migrationID, time.Since(migStartTime).Milliseconds())
			myPeer.Close() // Close the previous peer.
			myPeer = newPeer

			migrationID++
		}
	}

	fmt.Printf("After silo run we have %d peers\n", len(peers))

	return nil
}

// migrateNow migrates a VM locally.
func migrateNow(id int, log loggingtypes.Logger, met *testutil.DummyMetrics, conf RunConfig, peerFrom *peer.Peer, peerTo *peer.Peer, testDir string, snapDir string) error {
	log.Info().Int("id", id).Msg("STARTING A MIGRATION")

	readersFrom := make([]io.Reader, 0)
	writersFrom := make([]io.Writer, 0)
	readersTo := make([]io.Reader, 0)
	writersTo := make([]io.Writer, 0)

	numPipes := conf.NumPipes
	if numPipes < 1 {
		numPipes = 1
	}

	for i := 0; i < numPipes; i++ {
		r1, w1 := io.Pipe()
		r2, w2 := io.Pipe()

		readersFrom = append(readersFrom, r1)
		writersFrom = append(writersFrom, w2)
		readersTo = append(readersTo, r2)
		writersTo = append(writersTo, w1)
	}

	hooks := peer.MigrateToHooks{
		OnBeforeSuspend:          func() {},
		OnAfterSuspend:           func() {},
		OnAllMigrationsCompleted: func() {},
		OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
		GetXferCustomData:        func() []byte { return []byte{} },
	}

	devicesTo := make([]common.MigrateToDevice, 0)
	for _, n := range append(common.KnownNames, common.DeviceOCIName) {
		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: 0, //200,
			MinCycles:      0, //5,
			MaxCycles:      0, //20,
			CycleThrottle:  500 * time.Millisecond,
		})
	}

	// If we are using DirectMemory, we tweak the device group directly, to read directly.
	if conf.DirectMemory {
		di := peerFrom.GetDG().GetDeviceInformationByName(common.DeviceMemoryName)

		numBlocks := (di.Size + uint64(conf.BlockSize) - 1) / uint64(conf.BlockSize)
		unrequiredBlocks := make([]uint, numBlocks)
		for i := 0; i < int(numBlocks); i++ {
			unrequiredBlocks[i] = uint(i)
		}

		memProv, err := memory.NewProcessMemoryStorage(peerFrom.VMPid, "/memory", func() []uint { return unrequiredBlocks })
		if err != nil {
			return err
		}

		di.DirtyRemote.SetRemoteReadProv(memProv) // Read from the memory directly.
	}

	startMigration := time.Now()

	var wg sync.WaitGroup
	var sendingErr error
	wg.Add(1)
	go func() {
		log.Info().Msg("MigrateTo called")
		opts := &common.MigrateToOptions{
			Concurrency: conf.MigrationConcurrency,
			Compression: conf.MigrationCompression,
		}
		if opts.Concurrency == 0 {
			opts.Concurrency = 10
		}

		err := peerFrom.MigrateTo(context.TODO(), devicesTo, 5*time.Minute, opts, readersFrom, writersFrom, hooks)
		log.Info().Msg("MigrateTo completed")
		sendingErr = err
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
			log.Info().Msg("Completed migration")
			completedWg.Done()
		},
	}

	_, devicesFrom, err := getDevicesFrom(id, testDir, snapDir, conf)
	if err != nil {
		return err
	}

	err = peerTo.MigrateFrom(context.TODO(), devicesFrom, readersTo, writersTo, hooks2)
	if err != nil {
		return err
	}

	// Wait for migration to complete
	wg.Wait()

	evacuationTook := time.Since(startMigration)

	// Make sure we got the completion callback
	completedWg.Wait()

	migrationTook := time.Since(startMigration)

	if sendingErr != nil {
		return sendingErr
	}

	// Show some data on the migration...
	fmt.Printf(" === Evacuation took %dms Migration took %dms\n", evacuationTook.Milliseconds(), migrationTook.Milliseconds())

	proToPipe := met.GetProtocol(fmt.Sprintf("%s-%d", conf.Name, 0), "migrateToPipe")
	if proToPipe != nil {
		stats := proToPipe.GetMetrics()
		fmt.Printf(" === Migration %s bytes sent, %s bytes received\n", formatBytes(stats.DataSent), formatBytes(stats.DataRecv))
	}

	// If we don't do this, we can't use the same network etc
	err = peerFrom.CloseRuntime() // Only close the runtime, not the devices
	if err != nil {
		return err
	}

	// time.Sleep(10 * time.Second) // NOT IDEAL. What happens? FIXME.... networking? Or an issue closing runtime

	// Is there still some sort of issue?
	// Make sure the hashes are equal here...
	/*
		for _, dname := range []string{common.DeviceMemoryName, common.DeviceStateName} {
			dev1 := peerFrom.GetDG().GetDeviceInformationByName(dname)
			devProv1, err := sources.NewFileStorage(path.Join("/dev", dev1.Exp.Device()), int64(dev1.Size))
			if err != nil {
				fmt.Printf("ERROR reading %s dev %v\n", dname, err)
			}
			prov1 := dev1.Exp.GetProvider()

			dev2 := peerTo.GetDG().GetDeviceInformationByName(dname)
			devProv2, err := sources.NewFileStorage(path.Join("/dev", dev2.Exp.Device()), int64(dev2.Size))
			if err != nil {
				fmt.Printf("ERROR reading %s dev %v\n", dname, err)
			}
			prov2 := dev2.Exp.GetProvider()

			eq1, err1 := storage.Equals(prov1, prov2, 1024*1024)
			eq2, err2 := storage.Equals(devProv1, prov2, 1024*1024)
			eq3, err3 := storage.Equals(devProv2, prov1, 1024*1024)
			eq4, err4 := storage.Equals(devProv1, devProv2, 1024*1024)
			fmt.Printf("+ + + CHECK %s device(%v %v %v %v) %d bytes equals %t %t %t %t\n", dname, err1, err2, err3, err4, prov1.Size(), eq1, eq2, eq3, eq4)
		}
	*/
	err = peerTo.Resume(context.TODO(), 5*time.Minute, 5*time.Minute)

	if err != nil {
		return err
	}

	devTab := gotable.NewTable([]string{"Name",
		"Write", "Comp", "CompData", "Base",
		"AvailP2P", "AvailAlt",
	},
		[]int64{-16, 10, 10, 10, 10, 10, 10},
		"No data in table.")

	for _, n := range append(common.KnownNames, common.DeviceOCIName) {
		protoTo := met.GetToProtocol(fmt.Sprintf("%s-%d", conf.Name, 0), n)
		stTo := protoTo.GetMetrics()
		protoFrom := met.GetFromProtocol(fmt.Sprintf("%s-%d", conf.Name, 1), n)
		stFrom := protoFrom.GetMetrics()
		devTab.AppendRow([]interface{}{
			n,
			formatBytes(stTo.SentWriteAtBytes),
			formatBytes(stTo.SentWriteAtCompBytes),
			formatBytes(stTo.SentWriteAtCompDataBytes),
			formatBytes(stTo.SentYouAlreadyHaveBytes),
			formatBytes(uint64(len(stFrom.AvailableP2P) * int(conf.BlockSize))),
			formatBytes(uint64(len(stFrom.AvailableAltSources) * int(conf.BlockSize))),
		})
	}

	devTab.Print()

	return nil
}

// setupPeer sets up a new peer
func setupPeer(log loggingtypes.Logger, met metrics.SiloMetrics, conf RunConfig, testDir string, netns string, enableInput bool, enableOutput bool, instance int, runningCB func(bool)) (*peer.Peer, error) {

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return nil, err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return nil, err
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
		fout, err := os.OpenFile(fmt.Sprintf("%s-%04d.stdout", conf.Name, instance), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0660)
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
		GrabMemory:              hConf.NoMapShared,
		GrabFailsafe:            conf.GrabFailsafe,
		GrabUpdateMemory:        hConf.NoMapShared,
		RunningCB:               runningCB,
	}

	if conf.DirectMemory {
		rp.GrabUpdateDirty = true
		rp.GrabUpdateMemory = false

		// TODO: Tweak memory storage
	}

	// Use something to push output (sometimes needed)
	if !enableInput {
		pusherCtx, pusherCancel := context.WithCancel(context.Background())
		r := rfirecracker.NewOutputPusher(pusherCtx, log)
		rp.HypervisorConfiguration.Stdin = r
		rp.RunningCB = func(r bool) {
			runningCB(r)
			if !r {
				pusherCancel()
			}
		}
	}

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, met, nil, fmt.Sprintf("%s-%d", conf.Name, instance), rp)
	if err != nil {
		return nil, err
	}

	// NB: We set it here to get rid of the uuid prefix Peer adds.
	myPeer.SetInstanceID(fmt.Sprintf("%s-%d", conf.Name, instance))

	return myPeer, nil
}

// getDevicesFrom configures the silo devices
func getDevicesFrom(id int, testDir string, snapDir string, conf RunConfig) (map[string]*config.DeviceSchema, []common.MigrateFromDevice, error) {
	schemas := make(map[string]*config.DeviceSchema)
	devicesFrom := make([]common.MigrateFromDevice, 0)
	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:      n,
			BlockSize: conf.BlockSize,
			Shared:    false,
			AnyOrder:  !conf.UseVolatility,
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
			dev.SharedBase = conf.UseSharedBase
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

		// For now only enable S3 for disks
		if n == common.DeviceDiskName || n == common.DeviceOCIName {
			dev.S3Sync = conf.S3Sync
			dev.S3Secure = conf.S3Secure
			dev.S3SecretKey = conf.S3SecretKey
			dev.S3Endpoint = conf.S3Endpoint
			dev.S3Concurrency = conf.S3Concurrency
			dev.S3Bucket = conf.S3Bucket
			dev.S3AccessKey = conf.S3AccessKey

			dev.S3BlockShift = conf.S3BlockShift
			dev.S3OnlyDirty = conf.S3OnlyDirty
			dev.S3MaxAge = conf.S3MaxAge
			dev.S3MinChanged = conf.S3MinChanged
			dev.S3Limit = conf.S3Limit
			dev.S3CheckPeriod = conf.S3CheckPeriod

			/*
				dev.S3BlockShift = 2
				dev.S3OnlyDirty = false
				dev.S3MaxAge = "100ms"
				dev.S3MinChanged = 4
				dev.S3Limit = 256
				dev.S3CheckPeriod = "100ms"
			*/
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
