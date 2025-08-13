package peer

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/stretchr/testify/assert"
)

var testPeerDirCowOverlays = "test_overlays"

func TestPeerCowOverlaysMulti(t *testing.T) {

	err := os.Mkdir(testPeerDirCowOverlays, 0777)
	assert.NoError(t, err)

	t.Cleanup(func() {
		err := os.RemoveAll(testPeerDirCowOverlays)
		assert.NoError(t, err)
	})

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.DebugLevel)

	deviceSizes := map[string]int{
		"tester": 4 * 1024 * 1024,
	}

	// Create base file
	overlays := make(map[string]string)
	states := make(map[string]string)
	writeAlso := make(map[string]storage.Provider)
	for n, s := range deviceSizes {
		baseData := make([]byte, s)
		_, err = rand.Read(baseData)
		assert.NoError(t, err)
		err = os.WriteFile(path.Join(testPeerDirCowOverlays, fmt.Sprintf("%s_base", n)), baseData, 0660)
		assert.NoError(t, err)
		overlays[n] = ""
		states[n] = ""
		// Create some memory devices to check against
		writeAlso[n] = sources.NewMemoryStorage(s)
		_, err = writeAlso[n].WriteAt(baseData, 0)
		assert.NoError(t, err)
	}

	for i := 0; i < 10; i++ {

		// Add overlay to the devices
		for n := range deviceSizes {
			// Now add an extra overlay file
			if overlays[n] != "" {
				overlays[n] = overlays[n] + common.OverlaySep
				states[n] = states[n] + common.OverlaySep
			}
			overlays[n] = fmt.Sprintf("%s%s", overlays[n], path.Join(testPeerDirCowOverlays, fmt.Sprintf("%s_overlay_%d", n, i)))
			states[n] = fmt.Sprintf("%s%s", states[n], path.Join(testPeerDirCowOverlays, fmt.Sprintf("%s_state_%d", n, i)))
		}

		peer := startPeer(t, log, overlays, states, deviceSizes, writeAlso)
		time.Sleep(1 * time.Second) // Allow some writes to go through

		// Close the peer
		err = peer.Close()
		assert.NoError(t, err)

		// Remove any devices
		for n := range deviceSizes {
			err = os.Remove(path.Join(testPeerDirCowOverlays, n))
			assert.NoError(t, err)
		}
	}
}

func startPeer(t *testing.T, log types.Logger, overlays map[string]string, states map[string]string, deviceSizes map[string]int, writeAlso map[string]storage.Provider) *Peer {
	// Create a mock runtime, and start the peer.
	rp := &runtimes.MockRuntimeProvider{
		T:           t,
		HomePath:    testPeerDirCowOverlays,
		DoWrites:    true,
		DeviceSizes: deviceSizes,
		WriteAlso:   writeAlso,
	}
	peer, err := StartPeer(context.TODO(), context.Background(), log, nil, nil, "cow_test", rp)
	assert.NoError(t, err)

	hooks1 := MigrateFromHooks{
		OnLocalDeviceRequested:     func(id uint32, path string) {},
		OnLocalDeviceExposed:       func(id uint32, path string) {},
		OnLocalAllDevicesRequested: func() {},
		OnXferCustomData:           func(data []byte) {},
	}

	devicesFrom := make([]common.MigrateFromDevice, 0)

	for n := range deviceSizes {
		devicesFrom = append(devicesFrom, common.MigrateFromDevice{
			Name:          n,
			Base:          path.Join(testPeerDirCowOverlays, fmt.Sprintf("%s_base", n)),
			BlockSize:     1024 * 4,
			SharedBase:    true,
			Overlay:       overlays[n],
			State:         states[n],
			UseSparseFile: true,
		})
	}

	err = peer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
	assert.NoError(t, err)

	// Check it's as expected (eg that the view through Silo cow overlays, is equal to our flat view writeAlso)
	for n, s := range deviceSizes {
		fprov, err := sources.NewFileStorage(path.Join(testPeerDirCowOverlays, n), int64(s))
		assert.NoError(t, err)

		eq, err := storage.Equals(fprov, writeAlso[n], 4096)
		assert.NoError(t, err)
		assert.True(t, eq)
	}

	err = peer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
	assert.NoError(t, err)

	return peer
}
