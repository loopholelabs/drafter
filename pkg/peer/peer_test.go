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
	"github.com/stretchr/testify/assert"
)

const testPeerSource = "test_peer_source"
const testPeerDest = "test_peer_dest"

func setupDevices(t *testing.T) ([]common.MigrateFromDevice, []common.MigrateFromDevice, []common.MigrateToDevice, map[string]int) {
	err := os.Mkdir(testPeerSource, 0777)
	assert.NoError(t, err)
	err = os.Mkdir(testPeerDest, 0777)
	assert.NoError(t, err)

	t.Cleanup(func() {
		err := os.RemoveAll(testPeerSource)
		assert.NoError(t, err)
		err = os.RemoveAll(testPeerDest)
		assert.NoError(t, err)
	})

	devicesInit := make([]common.MigrateFromDevice, 0)
	devicesTo := make([]common.MigrateToDevice, 0)
	devicesFrom := make([]common.MigrateFromDevice, 0)

	deviceSizes := make(map[string]int, 0)

	// Create some device source files, and setup devicesTo and devicesFrom for migration.
	for _, n := range common.KnownNames {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dataSize := (1 + rand.Intn(5)) * 1024 * 1024
		buffer := make([]byte, dataSize)
		_, err = crand.Read(buffer)
		assert.NoError(t, err)
		err = os.WriteFile(path.Join(testPeerSource, fn), buffer, 0777)
		assert.NoError(t, err)

		deviceSizes[n] = dataSize

		devicesInit = append(devicesInit, common.MigrateFromDevice{
			Name:      n,
			Base:      path.Join(testPeerSource, fn),
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
			Base:      path.Join(testPeerDest, fn),
			Overlay:   "",
			State:     "",
			BlockSize: 1024 * 1024,
			Shared:    false,
		})

	}

	return devicesInit, devicesFrom, devicesTo, deviceSizes
}

func TestPeer(t *testing.T) {

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//	log.SetLevel(types.TraceLevel)

	devicesInit, devicesFrom, devicesTo, deviceSizes := setupDevices(t)

	completion1Called := make(chan struct{})
	rp := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    testPeerSource,
		DoWrites:    true,
		DeviceSizes: deviceSizes,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "peer_test", rp)
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

	rp2 := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    testPeerDest,
		DoWrites:    false,
		DeviceSizes: deviceSizes,
	}
	peer2, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "peer_test", rp2)
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
			buff1, err := os.ReadFile(path.Join(testPeerSource, n))
			assert.NoError(t, err)
			buff2, err := os.ReadFile(path.Join(testPeerDest, n))
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

/*
func TestPeerEarlyClose(t *testing.T) {

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.DebugLevel)

	devicesInit, devicesFrom, devicesTo, deviceSizes := setupDevices(t)

	rp := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    testPeerSource,
		DoWrites:    true,
		DeviceSizes: deviceSizes,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "peer_test", rp)
	assert.NoError(t, err)

	hooks1 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	err = peer.MigrateFrom(context.TODO(), devicesInit, nil, nil, hooks1)
	assert.NoError(t, err)

	err = peer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	// Now we have a "resumed peer"

	rp2 := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    testPeerDest,
		DoWrites:    false,
		DeviceSizes: deviceSizes,
	}
	peer2, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "peer_test", rp2)
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
	wg.Add(1)
	go func() {
		err := peer.MigrateTo(context.TODO(), devicesTo, 10*time.Second, 10, []io.Reader{r1}, []io.Writer{w2}, hooks)
		fmt.Printf("MigrateTo returned %v\n", err)
		// We expect there to be an error here...
		assert.Error(t, err)
		wg.Done()
	}()

	hooks2 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
		OnCompletion: func() {
			fmt.Printf("Completed!\n")
		},
	}
	err = peer2.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{r2}, []io.Writer{w1}, hooks2)
	assert.NoError(t, err)

	// CLOSE the connection on the receiving side before migration has completed.
	r2.Close()
	w1.Close()

	fmt.Printf("Waiting for wg\n")

	wg.Wait()

	// Make sure we can close the peers...
	err = peer2.Close()
	assert.NoError(t, err)

	err = peer.Close()
	assert.NoError(t, err)

}
*/
