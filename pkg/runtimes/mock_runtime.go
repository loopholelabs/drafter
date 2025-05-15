package runtimes

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/stretchr/testify/assert"
)

// A MockRuntimeProvider will periodically modify device(s) while it's running
type MockRuntimeProvider struct {
	T              *testing.T
	HomePath       string
	DoWrites       bool
	DeviceSizes    map[string]int
	writeContext   context.Context
	writeCancel    context.CancelFunc
	writeWaitGroup sync.WaitGroup
}

func (rp *MockRuntimeProvider) Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error {
	fmt.Printf(" ### Start %s\n", rp.HomePath)
	return nil
}

func (rp *MockRuntimeProvider) Close(dg *devicegroup.DeviceGroup) error {
	fmt.Printf(" ### Close %s\n", rp.HomePath)
	return rp.Suspend(context.TODO(), 10*time.Second, dg)
}

func (rp *MockRuntimeProvider) DevicePath() string {
	return rp.HomePath
}

func (rp *MockRuntimeProvider) GetVMPid() int {
	return 0
}

func (rp *MockRuntimeProvider) Suspend(ctx context.Context, timeout time.Duration, dg *devicegroup.DeviceGroup) error {
	fmt.Printf(" ### Suspend %s\n", rp.HomePath)

	if rp.writeCancel != nil {
		rp.writeCancel()         // Cancel the VM writer
		rp.writeWaitGroup.Wait() // Wait until it's done
		rp.writeCancel = nil
	}
	return nil
}

func (rp *MockRuntimeProvider) FlushData(ctx context.Context, dg *devicegroup.DeviceGroup) error {
	fmt.Printf(" ### FlushData %s\n", rp.HomePath)

	for _, devName := range common.KnownNames {
		fp, err := os.OpenFile(path.Join(rp.HomePath, devName), os.O_RDWR, 0777)
		assert.NoError(rp.T, err)

		err = fp.Sync()
		assert.NoError(rp.T, err)
		err = fp.Close()
		assert.NoError(rp.T, err)
	}

	// Shouldn't need anything here, but may need fs.Sync
	return nil
}

func (rp *MockRuntimeProvider) Resume(ctx context.Context, rescueTimeout time.Duration, errChan chan error) error {
	fmt.Printf(" ### Resume %s\n", rp.HomePath)

	for _, n := range common.KnownNames {
		buffer, err := os.ReadFile(path.Join(rp.HomePath, n))
		assert.NoError(rp.T, err)
		hash := sha256.Sum256(buffer)
		fmt.Printf(" # HASH # %s ~ %x\n", n, hash)
	}

	if rp.DoWrites {
		periodWrites := 400 * time.Millisecond

		// Setup something to write to the devices randomly
		rp.writeContext, rp.writeCancel = context.WithCancel(context.TODO())
		rp.writeWaitGroup.Add(1)
		go func() {
			defer rp.writeWaitGroup.Done()

			if rp.DoWrites {
				// TODO: Write to some devices randomly until the context is cancelled...

				for {
					dev := rand.Intn(len(common.KnownNames))
					devName := common.KnownNames[dev]
					// Lets change a byte in this device...
					fp, err := os.OpenFile(path.Join(rp.HomePath, devName), os.O_RDWR, 0777)
					assert.NoError(rp.T, err)

					size := rp.DeviceSizes[devName]
					data := make([]byte, 4096)
					_, err = crand.Read(data)
					assert.NoError(rp.T, err)
					offset := rand.Intn(size - len(data))

					fmt.Printf(" ### WriteAt %s %s offset %d\n", rp.HomePath, devName, offset)
					// Write some random data to the device...
					_, err = fp.WriteAt(data, int64(offset))
					assert.NoError(rp.T, err)

					err = fp.Sync()
					assert.NoError(rp.T, err)
					err = fp.Close()
					assert.NoError(rp.T, err)

					select {
					case <-rp.writeContext.Done():
						fmt.Printf(" ### Writer stopped\n")
						return
					case <-time.After(periodWrites):
						break
					}
				}

			}

		}()
	}
	return nil
}
