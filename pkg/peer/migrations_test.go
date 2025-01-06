package peer

import (
	"context"
	"crypto/rand"
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
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/stretchr/testify/assert"
)

func setupFromFs(t *testing.T) (string, *devicegroup.DeviceGroup) {
	// First lets setup a temp dir
	tdir, err := os.MkdirTemp("", "drafter_vm_*")
	assert.NoError(t, err)

	// Create a dummy file to hold data for Silo.
	data := make([]byte, 1024*1024*16) // 16M
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

	dg, err := migrateFromFS(nil, nil, tdir, devices)
	assert.NoError(t, err)

	// Check the devices look ok and contain what we think they should...
	// The devices should have been created as 'tdir/<name>'

	names := dg.GetAllNames()
	assert.Equal(t, 1, len(names))
	assert.Equal(t, "test", names[0])

	fd, err := os.Open(path.Join(tdir, "test"))
	assert.NoError(t, err)

	// Try reading the device...
	buffer := make([]byte, len(data))
	n, err := fd.ReadAt(buffer, 0)
	assert.Equal(t, len(data), n)
	assert.NoError(t, err)
	assert.Equal(t, data, buffer)

	err = fd.Close()
	assert.NoError(t, err)

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

	migrateWait.Add(1)
	go func() {
		tweak := func(index int, name string, schema string) string {
			fmt.Printf("Tweak %s\n", schema)
			s := strings.ReplaceAll(schema, tdir, tdir2)
			fmt.Printf(" -> %s\n", s)
			return s
		}

		dg2, err = migrateFromPipe(nil, nil, tdir2,
			context.TODO(), []io.Reader{r2}, []io.Writer{w1}, tweak)
		assert.NoError(t, err)

		// TODO: Check dg2 looks good...
		names := dg2.GetAllNames()
		assert.Equal(t, 1, len(names))
		assert.Equal(t, "test", names[0])

		// TODO: Read data from tdir2 and make sure it matches.

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

	err = migrateToPipe(context.TODO(), []io.Reader{r1}, []io.Writer{w2},
		dg, 100, progress, vmState, devices)
	assert.NoError(t, err)

	// Make sure it all completed
	migrateWait.Wait()
}
