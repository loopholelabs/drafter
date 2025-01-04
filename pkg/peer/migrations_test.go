package peer

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/user"
	"path"
	"testing"

	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
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

	dg, err := migrateFromFS(tdir, devices)
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

	err = dg.CloseAll()
	assert.NoError(t, err)

	t.Cleanup(func() {
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

	/*
		TODO: Finish this... We should migrate through a pipe

		tdir, dg := setupFromFs(t)

		migrateToPipe()

		migrateFromPipe()
	*/
}
