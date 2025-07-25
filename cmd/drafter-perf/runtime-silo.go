package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime/pprof"
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
	"github.com/loopholelabs/silo/pkg/storage/sources"
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
	S3Prefix      string `json:"s3prefix"`

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
	testDir string, snapDir string, ns func() (string, func(), error), forwards func(string) (func(), error), benchCB func(), conf *RunConfig,
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

	var myPeer *peer.Peer
	myPeer, err = setupPeer(log, met, conf, testDir, myNetNs, enableInput, enableOutput, 0, func(r bool) {})
	if err != nil {
		return err
	}

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

	err = myPeer.Resume(context.TODO(), 15*time.Minute, 15*time.Minute)
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
			fmt.Printf("Closing mid peer %s %s\n", myPeer.VMPath, myPeer.GetInstanceID())
			err = myPeer.Close() // Close the previous peer.
			if err != nil {
				fmt.Printf("Error closing peer %v\n", err)
			}
			myPeer = newPeer

			migrationID++
		}
	}

	fmt.Printf("After silo run we have %d peers\n", len(peers))

	fmt.Printf("Closing last peer %s %s\n", myPeer.VMPath, myPeer.GetInstanceID())
	err = myPeer.Close()
	if err != nil {
		fmt.Printf("Error closing VM %v\n", err)
	}

	return nil
}

// migrateNow migrates a VM locally.
func migrateNow(id int, log loggingtypes.Logger, met *testutil.DummyMetrics, conf *RunConfig, peerFrom *peer.Peer, peerTo *peer.Peer, testDir string, snapDir string) error {
	log.Info().Int("id", id).Msg("STARTING A MIGRATION")

	// Do some CPU profiling here...
	f, err := os.Create(fmt.Sprintf("%s-%d.prof", conf.Name, id))
	if err != nil {
		panic(err)
	}
	err = pprof.StartCPUProfile(f)
	if err != nil {
		panic(err)
	}
	defer func() {
		pprof.StopCPUProfile()
		f.Close()
	}()

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

	// On the sending side, lets look directly at the memory
	memFromProv, err := memory.NewProcessMemoryStorage(peerFrom.VMPid, "/memory", func() []uint { return []uint{} })
	/*
					memFromDI := peerFrom.GetDG().GetDeviceInformationByName(common.DeviceMemoryName)
		memFromProv, err := sources.NewFileStorage(path.Join("/dev", memFromDI.Exp.Device()), int64(memFromDI.Size))
	*/
	if err != nil {
		return err
	}

	// On the receiving end, we want to access the data through nbd.
	memToDI := peerTo.GetDG().GetDeviceInformationByName(common.DeviceMemoryName)
	memToProv, err := sources.NewFileStorage(path.Join("/dev", memToDI.Exp.Device()), int64(memToDI.Size))
	if err != nil {
		return err
	}

	// Check blocks

	bufferFrom := make([]byte, 1024*1024)
	bufferTo := make([]byte, 1024*1024)

	memSize := uint64(8 * 1024 * 1024 * 1024) // useable
	fmt.Printf("Memory size is from (%d bytes) to(%d bytes)\n", memFromProv.Size(), memToDI.Size)

	for offset := uint64(0); offset < memSize; offset += 1024 * 1024 {
		nFrom, err := memFromProv.ReadAt(bufferFrom, int64(offset))
		if err != nil {
			return err
		}
		nTo, err := memToProv.ReadAt(bufferTo, int64(offset))
		if err != nil {
			return err
		}
		if nFrom != nTo {
			fmt.Printf("MEMORY N %d %d\n", nFrom, nTo)
		}
		if !bytes.Equal(bufferFrom, bufferTo) {
			diff := 0
			for i := 0; i < len(bufferFrom); i++ {
				if bufferFrom[i] != bufferTo[i] {
					diff++
				}
			}
			fmt.Printf("MEMORY BYTES block at offset %d differ (%d)\n", offset, diff)
			return errors.New("CORRUPTION!")
		}
	}

	// Check that the destination devices are all complete (no waiting for blocks)
	for _, n := range append(common.KnownNames, common.DeviceOCIName) {
		di := peerTo.GetDG().GetDeviceInformationByName(n)
		n1, n2 := di.WaitingCacheLocal.Availability()
		met := di.From.GetMetrics()

		if n1 != n2 {
			fmt.Printf("AVAILABILITY %s %d %d | writeAt %d comp %d hash %d\n", n, n1, n2, met.RecvWriteAt, met.RecvWriteAtComp, met.RecvWriteAtHash)
			// Check up on why...
			fmt.Printf("From WriteAt %d WriteAtComp %d WriteAtHash %d WriteAtYouAlreadyHave %d\n",
				met.RecvWriteAt,
				met.RecvWriteAtComp,
				met.RecvWriteAtHash,
				met.RecvWriteAtYouAlreadyHave)

			fmt.Printf("Available P2P %v\n", met.AvailableP2P)
			fmt.Printf("Available Alt %v\n", met.AvailableAltSources)

			return errors.New("MISSING DATA!")
		}
	}

	// If we don't do this, we can't use the same network etc
	err = peerFrom.Close() //Runtime() // Only close the runtime, not the devices
	if err != nil {
		return err
	}

	// Show device stats here...
	ShowDeviceStats(met, fmt.Sprintf("%s-%d", conf.Name, id-1))

	err = peerTo.Resume(context.TODO(), 15*time.Minute, 15*time.Minute)

	if err != nil {
		peerTo.Close() // Close the VM here...
		return err
	}

	devTab := gotable.NewTable([]string{"Name",
		"Write", "Comp", "CompData", "Hash", "Base",
		"AvailP2P", "AvailAlt", "DupeP2P",
		"F.P2P", "F.Alt", "F.Both",
	},
		[]int64{-16, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
		"No data in table.")

	totalWrite := uint64(0)
	totalComp := uint64(0)
	totalCompData := uint64(0)
	totalHash := uint64(0)
	totalBase := uint64(0)
	totalAvailP2P := uint64(0)
	totalAvailAlt := uint64(0)
	totalDupeP2P := uint64(0)
	totalFP2P := uint64(0)
	totalFAlt := uint64(0)
	totalFBoth := uint64(0)

	for _, n := range append(common.KnownNames, common.DeviceOCIName) {
		protoTo := met.GetToProtocol(fmt.Sprintf("%s-%d", conf.Name, 0), n)
		stTo := protoTo.GetMetrics()
		protoFrom := met.GetFromProtocol(fmt.Sprintf("%s-%d", conf.Name, 1), n)
		stFrom := protoFrom.GetMetrics()

		sources := make([]int, stFrom.NumBlocks)
		for _, v := range stFrom.AvailableP2P {
			sources[v] = 2 // P2P
		}
		for _, v := range stFrom.AvailableAltSources {
			sources[v] |= 1 // AltSources
		}

		onlyAltSources := 0
		onlyP2P := 0
		inBoth := 0
		// Now count up where the data came from
		for _, v := range sources {
			if v == 2 {
				onlyP2P++
			} else if v == 1 {
				onlyAltSources++
			} else if v == 3 {
				inBoth++
			}
		}

		totalWrite += stTo.SentWriteAtBytes
		totalComp += stTo.SentWriteAtCompBytes
		totalCompData += stTo.SentWriteAtCompDataBytes
		totalHash += stTo.SentWriteAtHashBytes
		totalBase += stTo.SentYouAlreadyHaveBytes

		totalAvailP2P += uint64(len(stFrom.AvailableP2P) * int(conf.BlockSize))
		totalAvailAlt += uint64(len(stFrom.AvailableAltSources) * int(conf.BlockSize))
		totalDupeP2P += uint64(len(stFrom.DuplicateP2P) * int(conf.BlockSize))
		totalFP2P += uint64(onlyP2P * int(conf.BlockSize))
		totalFAlt += uint64(onlyAltSources * int(conf.BlockSize))
		totalFBoth += uint64(inBoth * int(conf.BlockSize))

		devTab.AppendRow([]interface{}{
			n,
			formatBytes(stTo.SentWriteAtBytes),
			formatBytes(stTo.SentWriteAtCompBytes),
			formatBytes(stTo.SentWriteAtCompDataBytes),
			formatBytes(stTo.SentWriteAtHashBytes),
			formatBytes(stTo.SentYouAlreadyHaveBytes),
			formatBytes(uint64(len(stFrom.AvailableP2P) * int(conf.BlockSize))),
			formatBytes(uint64(len(stFrom.AvailableAltSources) * int(conf.BlockSize))),
			formatBytes(uint64(len(stFrom.DuplicateP2P) * int(conf.BlockSize))),

			formatBytes(uint64(onlyP2P * int(conf.BlockSize))),
			formatBytes(uint64(onlyAltSources * int(conf.BlockSize))),
			formatBytes(uint64(inBoth * int(conf.BlockSize))),
		})
	}

	devTab.AppendRow([]interface{}{
		"total",
		formatBytes(totalWrite),
		formatBytes(totalComp),
		formatBytes(totalCompData),
		formatBytes(totalHash),
		formatBytes(totalBase),
		formatBytes(totalAvailP2P), formatBytes(totalAvailAlt), formatBytes(totalDupeP2P),
		formatBytes(totalFP2P), formatBytes(totalFAlt), formatBytes(totalFBoth),
	})

	devTab.Print()

	return nil
}

// setupPeer sets up a new peer
func setupPeer(log loggingtypes.Logger, met metrics.SiloMetrics, conf *RunConfig, testDir string, netns string, enableInput bool, enableOutput bool, instance int, runningCB func(bool)) (*peer.Peer, error) {

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
		DirectMemory:            conf.DirectMemory,
	}

	if conf.DirectMemory {
		rp.GrabUpdateDirty = true
		rp.GrabUpdateMemory = false
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
func getDevicesFrom(id int, testDir string, snapDir string, conf *RunConfig) (map[string]*config.DeviceSchema, []common.MigrateFromDevice, error) {
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

		if n == common.DeviceDiskName || n == common.DeviceOCIName || n == common.DeviceMemoryName {
			dev.S3Sync = conf.S3Sync
			dev.S3Secure = conf.S3Secure
			dev.S3SecretKey = conf.S3SecretKey
			dev.S3Endpoint = conf.S3Endpoint
			dev.S3Concurrency = conf.S3Concurrency
			dev.S3Bucket = conf.S3Bucket
			dev.S3AccessKey = conf.S3AccessKey
			dev.S3Prefix = conf.S3Prefix

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
