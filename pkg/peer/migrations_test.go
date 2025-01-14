package peer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/stretchr/testify/assert"
)

func readAllFromDevice(name string, size int) ([]byte, error) {
	fd, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	// Try reading the device...
	buffer := make([]byte, size)
	n, err := fd.ReadAt(buffer, 0)
	if n != size {
		return nil, errors.New("unable to read full data")
	}
	if err != nil {
		return nil, err
	}

	err = fd.Close()
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

const testFileSize = 16 * 1024 * 1024

func setupFromFs(t *testing.T) (string, *devicegroup.DeviceGroup) {
	// First lets setup a temp dir
	tdir, err := os.MkdirTemp("", "drafter_vm_*")
	assert.NoError(t, err)

	// Create a dummy file to hold data for Silo.
	data := make([]byte, testFileSize) // 16M
	_, err = rand.Read(data)
	assert.NoError(t, err)
	os.WriteFile(path.Join(tdir, "file_data"), data, 0777)

	devices := []MigrateFromDevice{
		{
			Name:      "test",
			Base:      path.Join(tdir, "file_data"),
			Overlay:   "",
			State:     "",
			BlockSize: 1024 * 1024,
			Shared:    false,
		},
	}

	tweak := func(_ int, _ string, s *config.DeviceSchema) *config.DeviceSchema {
		return s
	}

	dg, err := migrateFromFS(nil, nil, tdir, devices, tweak)
	assert.NoError(t, err)

	// Check the devices look ok and contain what we think they should...
	// The devices should have been created as 'tdir/<name>'

	names := dg.GetAllNames()
	assert.Equal(t, 1, len(names))
	assert.Equal(t, "test", names[0])

	ddata, err := readAllFromDevice(path.Join(tdir, "test"), len(data))
	assert.NoError(t, err)
	assert.Equal(t, data, ddata)

	t.Cleanup(func() {
		err = dg.CloseAll()
		assert.NoError(t, err)

		os.Remove(tdir)
	})

	return tdir, dg
}

func TestMigrateFromFs(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	if currentUser.Username != "root" {
		fmt.Printf("Cannot run test unless we are root.\n")
		return
	}

	setupFromFs(t)
}

func TestMigrateFromFsThenBetween(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	if currentUser.Username != "root" {
		fmt.Printf("Cannot run test unless we are root.\n")
		return
	}

	// First lets setup a temp dir
	tdir2, err := os.MkdirTemp("", "drafter_vm_*")
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.Remove(tdir2)
	})

	tdir, dg := setupFromFs(t)

	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()

	progress := func(p map[string]*migrator.MigrationProgress) {
		fmt.Printf("Progress...\n")
	}

	devices := []mounter.MigrateToDevice{
		{
			Name:           "test",
			MaxDirtyBlocks: 10,
			MinCycles:      4,
			MaxCycles:      10,
			CycleThrottle:  100 * time.Millisecond,
		},
	}

	var dg2 *devicegroup.DeviceGroup
	var migrateWait sync.WaitGroup

	var customDataLock sync.Mutex
	var customDataReceived []byte

	migrateWait.Add(1)
	go func() {
		tweak := func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema {
			schema.Location = strings.ReplaceAll(schema.Location, tdir, tdir2)
			return schema
		}

		cdh := func(cdata []byte) {
			customDataLock.Lock()
			customDataReceived = cdata
			customDataLock.Unlock()
			// cdata is custom xfer data
		}

		dg2, err = migrateFromPipe(nil, nil, tdir2,
			context.TODO(), []io.Reader{r2}, []io.Writer{w1}, tweak, cdh)
		assert.NoError(t, err)

		// TODO: Check dg2 looks good...
		names := dg2.GetAllNames()
		assert.Equal(t, 1, len(names))
		assert.Equal(t, "test", names[0])

		// TODO: Read data from tdir2 and make sure it matches.
		ddata1, err := readAllFromDevice(path.Join(tdir, "test"), testFileSize)
		assert.NoError(t, err)
		ddata2, err := readAllFromDevice(path.Join(tdir2, "test"), testFileSize)
		assert.NoError(t, err)

		assert.Equal(t, ddata1, ddata2)

		err = dg2.WaitForCompletion()
		assert.NoError(t, err)

		migrateWait.Done()
	}()

	suspendFunc := func(ctx context.Context, timeout time.Duration) error {
		fmt.Printf("suspendFunc\n")
		return nil
	}
	msyncFunc := func(ctx context.Context) error {
		fmt.Printf("msyncFunc\n")
		return nil
	}
	onBeforeSuspend := func() {
		fmt.Printf("onBeforeSuspend\n")
	}
	onAfterSuspend := func() {
		fmt.Printf("onAfterSuspend\n")
	}

	vmState := NewVMStateMgr(context.TODO(), suspendFunc, 100*time.Millisecond,
		msyncFunc, onBeforeSuspend, onAfterSuspend)

	customData := []byte("hello")

	getCustomPayload := func() []byte {
		fmt.Printf("getCustomPayload\n")
		return customData
	}

	err = migrateToPipe(context.TODO(), []io.Reader{r1}, []io.Writer{w2},
		dg, 100, progress, vmState, devices, getCustomPayload)
	assert.NoError(t, err)

	// Make sure it all completed
	migrateWait.Wait()

	customDataLock.Lock()
	assert.Equal(t, customData, customDataReceived)
	customDataLock.Unlock()

}
