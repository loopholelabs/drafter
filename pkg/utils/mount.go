package utils

import (
	"os"

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
	if err := os.MkdirAll(m.dir, os.ModePerm); err != nil {
		return err
	}

	return unix.Mount(m.devicePath, m.dir, "ext4", 0, "")
}

func (m *Mount) Close() error {
	fd, err := unix.Open(m.dir, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	if err := unix.Fsync(fd); err != nil {
		return err
	}

	if err := unix.Unmount(m.dir, 0); err != nil {
		return err
	}

	return os.RemoveAll(m.dir)
}
