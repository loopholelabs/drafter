package peer

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/stretchr/testify/assert"
)

// A MockRuntimeProvider will periodically modify device(s) while it's running
type MockRuntimeProvider struct {
	HomePath string
}

func (rp *MockRuntimeProvider) Start(ctx context.Context, rescueCtx context.Context) error {
	fmt.Printf(" ### Start %s\n", rp.HomePath)
	return nil
}

func (rp *MockRuntimeProvider) Close() error {
	fmt.Printf(" ### Close %s\n", rp.HomePath)
	return nil
}

func (rp *MockRuntimeProvider) DevicePath() string {
	fmt.Printf(" ### DevicePath %s\n", rp.HomePath)
	return rp.HomePath
}

func (rp *MockRuntimeProvider) GetVMPid() int {
	fmt.Printf(" ### GetVMPid %s\n", rp.HomePath)
	return 0
}

func (rp *MockRuntimeProvider) SuspendAndCloseAgentServer(ctx context.Context, timeout time.Duration) error {
	fmt.Printf(" ### SuspendAndCloseAgentServer %s\n", rp.HomePath)
	return nil
}

func (rp *MockRuntimeProvider) FlushData(ctx context.Context) error {
	fmt.Printf(" ### FlushData %s\n", rp.HomePath)
	return nil
}

func (rp *MockRuntimeProvider) Resume(resumeTimeout time.Duration, rescueTimeout time.Duration) error {
	fmt.Printf(" ### Resume %s\n", rp.HomePath)
	return nil
}

const testPeerSource = "test_peer_source"
const testPeerDest = "test_peer_dest"

func setupDevices(t *testing.T) ([]common.MigrateFromDevice, []common.MigrateFromDevice, []common.MigrateToDevice) {
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

	// Create some device source files, and setup devicesTo and devicesFrom for migration.
	for _, n := range common.KnownNames {
		// Create some initial devices...
		fn := common.DeviceFilenames[n]

		dataSize := (1 + rand.Intn(5)) * 1024 * 1024
		buffer := make([]byte, dataSize)
		err = os.WriteFile(path.Join(testPeerSource, fn), buffer, 0777)
		assert.NoError(t, err)

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

	return devicesInit, devicesFrom, devicesTo
}

func TestPeer(t *testing.T) {

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	//	log.SetLevel(types.TraceLevel)

	devicesInit, devicesFrom, devicesTo := setupDevices(t)

	rp := &MockRuntimeProvider{
		HomePath: testPeerSource,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, rp)
	assert.NoError(t, err)

	hooks1 := mounter.MigrateFromHooks{
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

	rp2 := &MockRuntimeProvider{
		HomePath: testPeerDest,
	}
	peer2, err := StartPeer(context.TODO(), context.Background(), log, rp2)
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
		assert.NoError(t, err)
		wg.Done()
	}()

	hooks2 := mounter.MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}
	err = peer2.MigrateFrom(context.TODO(), devicesFrom, []io.Reader{r2}, []io.Writer{w1}, hooks2)
	assert.NoError(t, err)

	wg.Wait()

	// Make sure everything migrated as expected...
	for _, n := range common.KnownNames {
		fn := common.DeviceFilenames[n]
		buff1, err := os.ReadFile(path.Join(testPeerSource, fn))
		assert.NoError(t, err)
		buff2, err := os.ReadFile(path.Join(testPeerDest, fn))
		assert.NoError(t, err)

		// Check the data is identical
		assert.Equal(t, buff1, buff2)
	}

	// Make sure we can close the peers...
	err = peer2.Close()
	assert.NoError(t, err)

	err = peer.Close()
	assert.NoError(t, err)

}
