package fc_tests

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
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/testutils"
	"github.com/stretchr/testify/assert"
)

const testPeerDirCowS3 = "test_peer_cow"
const enableS3 = false
const performHashChecks = false
const pauseWaitSecondsMax = 3

func setupDevicesCowS3(t *testing.T, log types.Logger) ([]common.MigrateToDevice, string, string) {

	s3port := testutils.SetupMinioWithExpiry(t.Cleanup, 120*time.Minute)
	s3Endpoint := fmt.Sprintf("localhost:%s", s3port)

	err := sources.CreateBucket(false, s3Endpoint, "silosilo", "silosilo", "silosilo")
	assert.NoError(t, err)

	devicesTo := make([]common.MigrateToDevice, 0)

	// create package files
	snapDir := setupSnapshot(t, log, context.Background())

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

func getDevicesFrom(t *testing.T, snapDir string, s3Endpoint string, i int) []common.MigrateFromDevice {
	devicesFrom := make([]common.MigrateFromDevice, 0)

	err := os.Mkdir(fmt.Sprintf("%s_%d", testPeerDirCowS3, i), 0777)
	assert.NoError(t, err)

	t.Cleanup(func() {
		err := os.RemoveAll(fmt.Sprintf("%s_%d", testPeerDirCowS3, i))
		assert.NoError(t, err)
	})

	for _, n := range append(common.KnownNames, "oci") {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dev := common.MigrateFromDevice{
			Name:       n,
			Base:       path.Join(snapDir, n),
			Overlay:    path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, i), fmt.Sprintf("%s.overlay", fn)),
			State:      path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, i), fmt.Sprintf("%s.state", fn)),
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

func TestMigration(t *testing.T) {

	err := os.Mkdir(testPeerDirCowS3, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		err := os.RemoveAll(testPeerDirCowS3)
		assert.NoError(t, err)
	})

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//log.SetLevel(types.DebugLevel)

	numMigrations := 100

	grandTotalBlocksP2P := 0
	grandTotalBlocksS3 := 0

	devicesTo, snapDir, s3Endpoint := setupDevicesCowS3(t, log)

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	rp := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
		Log: log,
		HypervisorConfiguration: rfirecracker.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  testPeerDirCowS3,
			UID:            0,
			GID:            0,
			NetNS:          "ark0",
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
	assert.NoError(t, err)

	hooks1 := peer.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	devicesFrom := getDevicesFrom(t, snapDir, s3Endpoint, 0)
	err = myPeer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
	assert.NoError(t, err)

	err = myPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	// Now we have a FIRST "resumed peer"

	// Lets send it on a migration journey...

	var lastPeer = myPeer

	for migration := 0; migration < numMigrations; migration++ {

		// Wait for some random time...
		waitTime := rand.Intn(pauseWaitSecondsMax)

		// Let some S3 sync go on...
		time.Sleep(time.Duration(waitTime) * time.Second)

		// Create a new RuntimeProvider
		rp2 := &rfirecracker.FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Log: log,
			HypervisorConfiguration: rfirecracker.HypervisorConfiguration{
				FirecrackerBin: firecrackerBin,
				JailerBin:      jailerBin,
				ChrootBaseDir:  testPeerDirCowS3,
				UID:            0,
				GID:            0,
				NetNS:          "ark0",
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
				fmt.Printf("Completed!\n")
				completedWg.Done()
			},
		}
		devicesFrom = getDevicesFrom(t, snapDir, s3Endpoint, migration+1)
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

		if performHashChecks && err == nil && sendingErr == nil {
			// Make sure everything migrated as expected...
			for _, n := range common.KnownNames {

				buff1, err := os.ReadFile(path.Join(rp.DevicePath(), n))
				assert.NoError(t, err)
				buff2, err := os.ReadFile(path.Join(rp2.DevicePath(), n))
				assert.NoError(t, err)

				// Compare hashes so we don't get tons of output if they do differ.
				hash1 := sha256.Sum256(buff1)
				hash2 := sha256.Sum256(buff2)

				fmt.Printf(" # Migration %d End hash %s ~ %x => %x\n", migration+1, n, hash1, hash2)

				// Check the data is identical
				assert.Equal(t, hash1, hash2)
			}
		}

		grandTotalBlocksP2P += totalBlocksP2P
		grandTotalBlocksS3 += totalBlocksS3

		// Close the last peer
		err = lastPeer.Close()
		assert.NoError(t, err)

		// We can resume here safely
		err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		assert.NoError(t, err)

		lastPeer = nextPeer

		fmt.Printf("\nMIGRATION %d COMPLETED evacuated in %dms migrated in %dms (%d P2P) (%d S3)\n\n", migration+1,
			evacuationTook.Milliseconds(), migrationTook.Milliseconds(), totalBlocksP2P, totalBlocksS3)
	}

	// Close the final peer
	err = lastPeer.Close()
	assert.NoError(t, err)

	// We would expect to have migrated data both via P2P, and also via S3.
	assert.Greater(t, grandTotalBlocksP2P, 0)
	//	assert.Greater(t, grandTotalBlocksS3, 0)

}
