//go:build !integration
// +build !integration

package peer

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/testutils"
	"github.com/stretchr/testify/assert"
)

const testPeerDirCowS3 = "test_peer_cow"

func setupDevicesCowS3(t *testing.T, num int) ([]common.MigrateToDevice, [][]common.MigrateFromDevice, map[string]int) {
	s3port := testutils.SetupMinio(t.Cleanup)
	s3Endpoint := fmt.Sprintf("localhost:%s", s3port)

	err := sources.CreateBucket(false, s3Endpoint, "silosilo", "silosilo", "silosilo")
	assert.NoError(t, err)

	for n := 0; n < num; n++ {
		err := os.Mkdir(fmt.Sprintf("%s_%d", testPeerDirCowS3, n), 0777)
		assert.NoError(t, err)

		t.Cleanup(func() {
			err := os.RemoveAll(fmt.Sprintf("%s_%d", testPeerDirCowS3, n))
			assert.NoError(t, err)
		})
	}

	devicesTo := make([]common.MigrateToDevice, 0)
	devicesFrom := make([][]common.MigrateFromDevice, num)

	for n := 0; n < num; n++ {
		devicesFrom[n] = make([]common.MigrateFromDevice, 0)
	}

	deviceSizes := make(map[string]int, 0)

	// Create some device source files, and setup devicesTo and devicesFrom for migration.
	for _, n := range common.KnownNames {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dataSize := (1 + rand.Intn(5)) * 1024 * 1024
		buffer := make([]byte, dataSize)
		_, err := crand.Read(buffer)
		assert.NoError(t, err)
		err = os.WriteFile(path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, 0), fn), buffer, 0777)
		assert.NoError(t, err)

		deviceSizes[n] = dataSize

		for i := 0; i < num; i++ {
			devicesFrom[i] = append(devicesFrom[i], common.MigrateFromDevice{
				Name:          n,
				Base:          path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, 0), fn),
				Overlay:       path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, i), fmt.Sprintf("%s.overlay", fn)),
				State:         path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, i), fmt.Sprintf("%s.state", fn)),
				BlockSize:     64 * 1024,
				Shared:        false,
				SharedBase:    true,
				S3Sync:        true,
				S3AccessKey:   "silosilo",
				S3SecretKey:   "silosilo",
				S3Endpoint:    s3Endpoint,
				S3Secure:      false,
				S3Bucket:      "silosilo",
				S3Concurrency: 10,

				S3BlockShift:  2,
				S3OnlyDirty:   false,
				S3MaxAge:      "100ms",
				S3MinChanged:  4,
				S3Limit:       256,
				S3CheckPeriod: "100ms",
			})
		}

		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: 10,
			MinCycles:      1,
			MaxCycles:      3,
			CycleThrottle:  1 * time.Second,
		})

	}
	return devicesTo, devicesFrom, deviceSizes
}

func TestPeerCowS3Multi(t *testing.T) {

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	// log.SetLevel(types.DebugLevel)

	numMigrations := 10

	grandTotalBlocksP2P := 0
	grandTotalBlocksS3 := 0

	devicesTo, devicesFrom, deviceSizes := setupDevicesCowS3(t, 1+numMigrations)

	for n, s := range deviceSizes {
		fmt.Printf("Size %s - %d\n", n, s)
	}

	rp := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    fmt.Sprintf("%s_%d", testPeerDirCowS3, 0),
		DoWrites:    true,
		DeviceSizes: deviceSizes,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp)
	assert.NoError(t, err)

	hooks1 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	err = peer.MigrateFrom(context.TODO(), devicesFrom[0], nil, nil, hooks1)
	assert.NoError(t, err)

	err = peer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	// Now we have a FIRST "resumed peer"

	// Lets send it on a migration journey...

	var lastPeer = peer

	for migration := 0; migration < numMigrations; migration++ {

		// Wait for some random time...
		waitTime := rand.Intn(15)

		// Let some S3 sync go on...
		time.Sleep(time.Duration(waitTime) * time.Second)

		// Create a new RuntimeProvider
		rp2 := &runtimes.MockRuntimeProvider{
			T:           t,
			HomePath:    fmt.Sprintf("%s_%d", testPeerDirCowS3, migration+1),
			DoWrites:    true,
			DeviceSizes: deviceSizes,
		}
		nextPeer, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp2)
		assert.NoError(t, err)

		r1, w1 := io.Pipe()
		r2, w2 := io.Pipe()

		hooks := MigrateToHooks{
			OnBeforeSuspend:          func() {},
			OnAfterSuspend:           func() {},
			OnAllMigrationsCompleted: func() {},
			OnProgress:               func(p map[string]*migrator.MigrationProgress) {},
			GetXferCustomData:        func() []byte { return []byte{} },
		}

		var wg sync.WaitGroup
		var sendingErr error
		wg.Add(1)
		go func() {
			err := lastPeer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, &common.MigrateToOptions{Concurrency: 10}, []io.Reader{r1}, []io.Writer{w2}, hooks)
			assert.NoError(t, err)
			sendingErr = err

			// Close the connection from here...
			// Close from the sending side
			_ = r1.Close()
			_ = w2.Close()

			wg.Done()
		}()

		var completedWg sync.WaitGroup
		completedWg.Add(1)
		hooks2 := MigrateFromHooks{
			OnLocalDeviceRequested:     func(id uint32, path string) {},
			OnLocalDeviceExposed:       func(id uint32, path string) {},
			OnLocalAllDevicesRequested: func() {},
			OnXferCustomData:           func(data []byte) {},
			OnCompletion: func() {
				fmt.Printf("Completed!\n")
				completedWg.Done()
			},
		}
		err = nextPeer.MigrateFrom(context.TODO(), devicesFrom[1+migration], []io.Reader{r2}, []io.Writer{w1}, hooks2)
		assert.NoError(t, err)

		// Wait for sending side to complete
		wg.Wait()

		// Make sure we got the completion callback
		completedWg.Wait()

		if err == nil && sendingErr == nil {
			// Make sure everything migrated as expected...
			for _, n := range common.KnownNames {
				buff1, err := os.ReadFile(path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, migration), n))
				assert.NoError(t, err)
				buff2, err := os.ReadFile(path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, migration+1), n))
				assert.NoError(t, err)

				// Compare hashes so we don't get tons of output if they do differ.
				hash1 := sha256.Sum256(buff1)
				hash2 := sha256.Sum256(buff2)

				fmt.Printf(" # Migration %d End hash %s ~ %x => %x\n", migration+1, n, hash1, hash2)

				// Check the data is identical
				assert.Equal(t, hash1, hash2)
			}
		}

		// Tot up some data...
		totalBlocksP2P := 0
		totalBlocksS3 := 0
		for _, n := range nextPeer.dg.GetAllNames() {
			di := nextPeer.dg.GetDeviceInformationByName(n)
			me := di.From.GetMetrics()
			totalBlocksP2P += len(me.AvailableP2P)
			totalBlocksS3 += len(me.AvailableAltSources)
		}

		fmt.Printf("Migration blocks %d p2p, %d s3\n", totalBlocksP2P, totalBlocksS3)
		grandTotalBlocksP2P += totalBlocksP2P
		grandTotalBlocksS3 += totalBlocksS3

		// We can resume here, and will start writes again.
		err = nextPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		assert.NoError(t, err)

		// Make sure we can close the last peer.
		for _, devName := range common.KnownNames {
			_ = os.Remove(path.Join(fmt.Sprintf("%s_%d", testPeerDirCowS3, migration), devName))
		}

		err = lastPeer.Close()
		assert.NoError(t, err)

		lastPeer = nextPeer

		fmt.Printf("\nMIGRATION %d COMPLETED\n\n", migration+1)
	}

	// Close the final peer
	err = lastPeer.Close()
	assert.NoError(t, err)

	// We would expect to have migrated data both via P2P, and also via S3.
	assert.Greater(t, grandTotalBlocksP2P, 0)
	assert.Greater(t, grandTotalBlocksS3, 0)

}
