package mount

import (
	"golang.org/x/sys/unix"
)

type EXT4Mount struct {
	dir        string
	devicePath string
}

func NewEXT4Mount(devicePath, dir string) *EXT4Mount {
	return &EXT4Mount{devicePath: devicePath, dir: dir}
}

func (m *EXT4Mount) Open() error {
	return unix.Mount(m.devicePath, m.dir, "ext4", 0, "")
}

func (m *EXT4Mount) Close() error {
	// No need to sync here; `unmount` automatically syncs the filesystem
	return unix.Unmount(m.dir, 0)
}
