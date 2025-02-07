package peer

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/stretchr/testify/assert"
)

const testReplayPeerSource = "test_replay_source"
const testReplayPeerDest = "test_replay_dest"

func setupReplayDevices(t *testing.T, deviceSizes map[string]int) ([]common.MigrateFromDevice, []common.MigrateFromDevice, []common.MigrateToDevice) {
	err := os.Mkdir(testReplayPeerSource, 0777)
	assert.NoError(t, err)
	err = os.Mkdir(testReplayPeerDest, 0777)
	assert.NoError(t, err)

	t.Cleanup(func() {
		err := os.RemoveAll(testReplayPeerSource)
		assert.NoError(t, err)
		err = os.RemoveAll(testReplayPeerDest)
		assert.NoError(t, err)
	})

	devicesInit := make([]common.MigrateFromDevice, 0)
	devicesTo := make([]common.MigrateToDevice, 0)
	devicesFrom := make([]common.MigrateFromDevice, 0)

	// Create some device source files, and setup devicesTo and devicesFrom for migration.
	for _, n := range common.KnownNames {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dataSize := deviceSizes[n]
		buffer := make([]byte, dataSize)
		_, err = crand.Read(buffer)
		assert.NoError(t, err)
		err = os.WriteFile(path.Join(testReplayPeerSource, fn), buffer, 0777)
		assert.NoError(t, err)

		deviceSizes[n] = dataSize

		devicesInit = append(devicesInit, common.MigrateFromDevice{
			Name:      n,
			Base:      path.Join(testReplayPeerSource, fn),
			Overlay:   "",
			State:     "",
			BlockSize: 1024 * 1024,
			Shared:    false,
		})

		devicesTo = append(devicesTo, common.MigrateToDevice{
			Name:           n,
			MaxDirtyBlocks: 10,
			MinCycles:      1,
			MaxCycles:      3,
			CycleThrottle:  1 * time.Second,
		})

		devicesFrom = append(devicesFrom, common.MigrateFromDevice{
			Name:      n,
			Base:      path.Join(testReplayPeerDest, fn),
			Overlay:   "",
			State:     "",
			BlockSize: 1024 * 1024,
			Shared:    false,
		})

	}

	return devicesInit, devicesFrom, devicesTo
}

func TestPeerReplayValkey(t *testing.T) {

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.DebugLevel)

	deviceSizes := map[string]int{
		common.DeviceConfigName: 8192,
		common.DeviceMemoryName: 1073741824,
		common.DeviceOCIName:    2388238336,
		common.DeviceDiskName:   402653184,
		common.DeviceStateName:  16384,
		common.DeviceKernelName: 29949952,
	}

	devicesInit, devicesFrom, devicesTo := setupReplayDevices(t, deviceSizes)

	completion1Called := make(chan struct{})
	rp := &runtimes.ReplayRuntimeProvider{
		HomePath:    testReplayPeerSource,
		ReplayPath:  "test_data/valkey_gz",
		Zipped:      true,
		DeviceSizes: deviceSizes,
		Speed:       1,
		Log:         log,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, nil, rp)
	assert.NoError(t, err)

	hooks1 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
		OnCompletion: func() {
			fmt.Printf("Completed peer1!\n")
			close(completion1Called)
		},
	}

	err = peer.MigrateFrom(context.TODO(), devicesInit, nil, nil, hooks1)
	assert.NoError(t, err)

	err = peer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	// Get test deadline context for first completion check
	deadline1, ok := t.Deadline()
	if !ok {
		deadline1 = time.Now().Add(1 * time.Minute) // Fallback if no deadline set
	}
	ctx1, cancel1 := context.WithDeadline(context.Background(), deadline1)
	defer cancel1()

	// Wait for first completion or test deadline
	select {
	case <-completion1Called:
		// OnCompletion was called as expected for peer1
	case <-ctx1.Done():
		t.Fatal("OnCompletion was not called before test deadline for peer1")
	}

	// Now we have a "resumed peer"

	rp2 := &runtimes.ReplayRuntimeProvider{
		HomePath:    testReplayPeerDest,
		ReplayPath:  "test_data/valkey",
		Zipped:      false,
		DeviceSizes: deviceSizes,
		Speed:       1,
		Log:         log,
	}
	peer2, err := StartPeer(context.TODO(), context.Background(), log, nil, rp2)
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
		err := peer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, 10, []io.Reader{r1}, []io.Writer{w2}, hooks)
		assert.NoError(t, err)
		sendingErr = err
		wg.Done()
	}()

	completion2Called := make(chan struct{})
	hooks2 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
		OnCompletion: func() {
			fmt.Printf("Completed peer2!\n")
			close(completion2Called)
		},
	}

	err = peer2.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{r2}, []io.Writer{w1}, hooks2)
	assert.NoError(t, err)

	// Wait for migration to complete
	wg.Wait()

	// Get test deadline context for second completion check
	deadline2, ok := t.Deadline()
	if !ok {
		deadline2 = time.Now().Add(1 * time.Minute) // Fallback if no deadline set
	}
	ctx2, cancel2 := context.WithDeadline(context.Background(), deadline2)
	defer cancel2()

	// Wait for second completion or test deadline
	select {
	case <-completion2Called:
		// OnCompletion was called as expected for peer2
	case <-ctx2.Done():
		t.Fatal("OnCompletion was not called before test deadline for peer2")
	}

	if err == nil && sendingErr == nil {
		// Make sure everything migrated as expected...
		for _, n := range common.KnownNames {
			buff1, err := os.ReadFile(path.Join(testReplayPeerSource, n))
			assert.NoError(t, err)
			buff2, err := os.ReadFile(path.Join(testReplayPeerDest, n))
			assert.NoError(t, err)

			// Compare hashes so we don't get tons of output if they do differ.
			hash1 := sha256.Sum256(buff1)
			hash2 := sha256.Sum256(buff2)

			fmt.Printf(" # End hash %s ~ %x\n", n, hash1)

			// Check the data is identical
			assert.Equal(t, hash1, hash2)
		}
	}
	// Make sure we can close the peers...
	err = peer2.Close()
	assert.NoError(t, err)

	err = peer.Close()
	assert.NoError(t, err)

}
