package utils

import (
	"os"

	"github.com/freddierice/go-losetup/v2"
	"golang.org/x/sys/unix"
)

type LoopMount struct {
	file string
	dir  string
	loop *losetup.Device
}

func NewLoopMount(file, dir string) *LoopMount {
	return &LoopMount{
		file: file,
		dir:  dir,
	}
}

func (l *LoopMount) Open() error {
	loop, err := losetup.Attach(l.file, 0, false)
	if err != nil {
		return err
	}
	l.loop = &loop

	if err := os.MkdirAll(l.dir, os.ModePerm); err != nil {
		return err
	}

	return unix.Mount(l.loop.Path(), l.dir, "ext4", 0, "")
}

func (l *LoopMount) Close() error {
	fd, err := unix.Open(l.dir, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}

	if err := unix.Fsync(fd); err != nil {
		return err
	}

	if err := unix.Close(fd); err != nil {
		return err
	}

	if err := unix.Unmount(l.dir, 0); err != nil {
		return err
	}

	if err := os.RemoveAll(l.dir); err != nil {
		return err
	}

	if l.loop != nil {
		if err := l.loop.Detach(); err != nil {
			return err
		}
	}

	return nil
}
