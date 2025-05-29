//go:build integration
// +build integration

package firecracker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/testutils"
	"github.com/stretchr/testify/assert"
)

const testPeerDirCowS3 = "test_peer_cow"

func TestMigrationBasicHashChecks(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      1,
		maxCycles:      1,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     true,
	})
}

func TestMigrationBasicHashChecksSoftDirty(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      1,
		maxCycles:      1,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     true,
		noMapShared:    true,
	})
}

func TestMigrationBasicWithS3(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      1,
		maxCycles:      1,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       true,
		hashChecks:     false,
	})
}

func TestMigration4Cpus(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      1,
		maxCycles:      1,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       4,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     false,
	})
}

func TestMigrationNoPause(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      1,
		maxCycles:      1,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   0,
		enableS3:       false,
		hashChecks:     false,
	})
}

func TestMigrationMultiCycle(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      10,
		maxCycles:      20,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     false,
	})
}

func TestMigrationNoCycle(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      0,
		maxCycles:      0,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     false,
	})
}

func TestMigrationMultiCycleSoftDirty(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      10,
		maxCycles:      20,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     false,
		noMapShared:    true,
	})
}

func TestMigrationNoCycleSoftDirty(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      0,
		maxCycles:      0,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   3 * time.Second,
		enableS3:       false,
		hashChecks:     false,
		noMapShared:    true,
	})
}

func TestMigrationNoCycleSoftDirty1s(t *testing.T) {
	migration(t, &migrationConfig{
		numMigrations:  5,
		minCycles:      0,
		maxCycles:      0,
		cycleThrottle:  100 * time.Millisecond,
		maxDirtyBlocks: 10,
		cpuCount:       1,
		memorySize:     1024,
		pauseWaitMax:   10 * time.Second,
		enableS3:       false,
		hashChecks:     false,
		noMapShared:    true,
		grabInterval:   time.Second,
	})
}

type migrationConfig struct {
	numMigrations  int
	minCycles      int
	maxCycles      int
	cycleThrottle  time.Duration
	maxDirtyBlocks int
	cpuCount       int
	memorySize     int
	pauseWaitMax   time.Duration
	enableS3       bool
	hashChecks     bool
	noMapShared    bool
	grabInterval   time.Duration
}

/**
 *
 * firecracker needs to work
 * blueprints expected to exist at ./out/blueprint
 *
 */

func setupDevicesCowS3(t *testing.T, log types.Logger, netns string, config *migrationConfig) ([]common.MigrateToDevice, string, string) {

	s3port := testutils.SetupMinioWithExpiry(t.Cleanup, 120*time.Minute)
	s3Endpoint := fmt.Sprintf("localhost:%s", s3port)

	err := sources.CreateBucket(false, s3Endpoint, "silosilo", "silosilo", "silosilo")
	assert.NoError(t, err)

	devicesTo := make([]common.MigrateToDevice, 0)

	template, err := GetCPUTemplate()
	assert.NoError(t, err)

	log.Info().Str("template", template).Msg("using cpu template")

	bootargs := DefaultBootArgsNoPVM
	ispvm, err := IsPVMHost()
	assert.NoError(t, err)
	if ispvm {
		bootargs = DefaultBootArgs
	}

	// create package files
	snapDir := setupSnapshot(t, log, context.Background(), netns, VMConfiguration{
		CPUCount:    int64(config.cpuCount),
		MemorySize:  int64(config.memorySize),
		CPUTemplate: template,
		BootArgs:    bootargs,
	},
	)

	for _, n := range append(common.KnownNames, "oci") {
		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: config.maxDirtyBlocks,
			MinCycles:      config.minCycles,
			MaxCycles:      config.maxCycles,
			CycleThrottle:  config.cycleThrottle,
		})

	}
	return devicesTo, snapDir, s3Endpoint
}

func getDevicesFrom(t *testing.T, snapDir string, s3Endpoint string, i int, config *migrationConfig) []common.MigrateFromDevice {
	devicesFrom := make([]common.MigrateFromDevice, 0)

	err := os.Mkdir(path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i)), 0777)
	assert.NoError(t, err)

	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			Base:       path.Join(snapDir, n),
			Overlay:    path.Join(path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.overlay", fn))),
			State:      path.Join(path.Join(testPeerDirCowS3, fmt.Sprintf("migration_%d", i), fmt.Sprintf("%s.state", fn))),
			BlockSize:  1024 * 1024,
			Shared:     false,
			SharedBase: true,
		}

		if config.enableS3 && (n == common.DeviceMemoryName || n == common.DeviceDiskName) {
			dev.S3Sync = true
			dev.S3AccessKey = "silosilo"
			dev.S3SecretKey = "silosilo"
			dev.S3Endpoint = s3Endpoint
			dev.S3Secure = false
			dev.S3Bucket = "silosilo"
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

func migration(t *testing.T, config *migrationConfig) {

	err := os.Mkdir(testPeerDirCowS3, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		err := os.RemoveAll(testPeerDirCowS3)
		assert.NoError(t, err)
	})

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//	log.SetLevel(types.TraceLevel)

	ns := testutil.SetupNAT(t, "", "dra")

	netns, err := ns.ClaimNamespace()
	assert.NoError(t, err)

	grandTotalBlocksP2P := 0
	grandTotalBlocksS3 := 0

	devicesTo, snapDir, s3Endpoint := setupDevicesCowS3(t, log, netns, config)

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	rp := &FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log: log,
		HypervisorConfiguration: FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  testPeerDirCowS3,
			UID:            0,
			GID:            0,
			NetNS:          netns,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         os.Stdout,
			Stderr:         os.Stderr,
			Stdin:          nil,
			NoMapShared:    config.noMapShared,
		},
		StateName:        common.DeviceStateName,
		MemoryName:       common.DeviceMemoryName,
		AgentServerLocal: struct{}{},
		Grabbing:         config.noMapShared,
	}

	rp.GrabInterval = config.grabInterval

	myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp)
	assert.NoError(t, err)

	hooks1 := peer.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	devicesFrom := getDevicesFrom(t, snapDir, s3Endpoint, 0, config)
	err = myPeer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
	assert.NoError(t, err)

	err = myPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	// Now we have a FIRST "resumed peer"

	// Lets send it on a migration journey...

	var lastPeer = myPeer

	for migration := 0; migration < config.numMigrations; migration++ {

		var shutdownTime time.Time
		var resumeTime time.Time

		// Wait for some random time...
		if config.pauseWaitMax.Milliseconds() > 0 {
			waitTime := rand.Intn(int(config.pauseWaitMax.Milliseconds()))
			time.Sleep(time.Duration(waitTime) * time.Millisecond)
		}

		rp.RunningCB = func(r bool) {
			if !r {
				// This peer is shutting down. Record the time that happened.
				shutdownTime = time.Now()
			}
		}

		lastrp := rp

		// Create a new RuntimeProvider
		rp = &FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Log: log,
			HypervisorConfiguration: FirecrackerMachineConfig{
				FirecrackerBin: firecrackerBin,
				JailerBin:      jailerBin,
				ChrootBaseDir:  testPeerDirCowS3,
				UID:            0,
				GID:            0,
				NetNS:          netns,
				NumaNode:       0,
				CgroupVersion:  2,
				Stdout:         os.Stdout,
				Stderr:         os.Stderr,
				Stdin:          nil,
				NoMapShared:    config.noMapShared,
			},
			StateName:        common.DeviceStateName,
			MemoryName:       common.DeviceMemoryName,
			AgentServerLocal: struct{}{},
			Grabbing:         config.noMapShared,
		}

		rp.GrabInterval = config.grabInterval

		// We can push output here if we need to...
		// opCtx, opCancel := context.WithCancel(context.TODO())
		// rp.HypervisorConfiguration.Stdin = NewOutputPusher(opCtx, log)

		rp.RunningCB = func(r bool) {
			if r {
				// This peer resumed. Record the time that happened
				resumeTime = time.Now()
			} else {
				//		opCancel() // Stop pushing output
			}
		}

		nextPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp)
		assert.NoError(t, err)

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
			assert.NoError(t, err)
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
				completedWg.Done()
			},
		}
		devicesFrom = getDevicesFrom(t, snapDir, s3Endpoint, migration+1, config)
		err = nextPeer.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{r2}, []io.Writer{w1}, hooks2)
		assert.NoError(t, err)

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

		if config.hashChecks && err == nil && sendingErr == nil {
			// Make sure everything migrated as expected...
			for _, n := range append(common.KnownNames, "oci") {

				// If we're doing soft dirty, it doesn't go through NBD, so we should do the comparison in silo provider...
				if rp.HypervisorConfiguration.NoMapShared && n == common.DeviceMemoryName {
					prov1 := lastPeer.GetDG().GetDeviceInformationByName(n).Exp.GetProvider()
					prov2 := nextPeer.GetDG().GetDeviceInformationByName(n).Exp.GetProvider()
					eq, err := storage.Equals(prov1, prov2, 1024*1024)
					assert.NoError(t, err)
					assert.True(t, eq)
					fmt.Printf(" # Migration %d End hash %s ok\n", migration+1, n)
				} else {

					buff1, err := os.ReadFile(path.Join(lastrp.DevicePath(), n))
					assert.NoError(t, err)
					buff2, err := os.ReadFile(path.Join(rp.DevicePath(), n))
					assert.NoError(t, err)

					// Compare hashes so we don't get tons of output if they do differ.
					hash1 := sha256.Sum256(buff1)
					hash2 := sha256.Sum256(buff2)

					fmt.Printf(" # Migration %d End hash %s ~ %x => %x\n", migration+1, n, hash1, hash2)

					// Check the data is identical
					assert.Equal(t, hash1, hash2)
				}
			}
		}

		grandTotalBlocksP2P += totalBlocksP2P
		grandTotalBlocksS3 += totalBlocksS3

		// Close the last peer
		err = lastPeer.Close()
		assert.NoError(t, err)

		pMetrics := lastPeer.GetMetrics()

		// We can resume here safely
		err = nextPeer.Resume(context.TODO(), 30*time.Second, 30*time.Second)
		assert.NoError(t, err)

		lastPeer = nextPeer

		downtimeString := ""
		if !resumeTime.IsZero() && !shutdownTime.IsZero() {
			downtimeString = resumeTime.Sub(shutdownTime).String()
		}

		fmt.Printf("MIGRATION %d COMPLETED evacuated in %dms migrated in %dms (%d P2P) (%d S3) (%d flush ops in %dms) downtime %s\n", migration+1,
			evacuationTook.Milliseconds(), migrationTook.Milliseconds(), totalBlocksP2P, totalBlocksS3, pMetrics.FlushDataOps, pMetrics.FlushDataTimeMs, downtimeString)
	}

	// Close the final peer
	err = lastPeer.Close()
	assert.NoError(t, err)

	// We would expect to have migrated data both via P2P, and also via S3.
	assert.Greater(t, grandTotalBlocksP2P, 0)
	//	assert.Greater(t, grandTotalBlocksS3, 0)

}
