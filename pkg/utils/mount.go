package utils

import (
	"golang.org/x/sys/unix"
)

type Mount struct {
	dir        string
	devicePath string
}

func NewMount(devicePath, dir string) *Mount {
	return &Mount{devicePath: devicePath, dir: dir}
}

func (m *Mount) Open() error {
	return unix.Mount(m.devicePath, m.dir, "ext4", 0, "")
}

func (m *Mount) Close() error {
	// No need to sync here; `unmount` automatically syncs the filesystem
	return unix.Unmount(m.dir, 0)
}
