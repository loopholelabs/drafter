package mount

import (
	"errors"
	"os"
	"syscall"

	"github.com/freddierice/go-losetup/v2"
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get device stat")
)

type LoopMount struct {
	file   string
	device *losetup.Device
}

func NewLoopMount(file string) *LoopMount {
	return &LoopMount{file: file}
}

func (l *LoopMount) Open() (int, error) {
	device, err := losetup.Attach(l.file, 0, false)
	if err != nil {
		return 0, err
	}

	l.device = &device

	info, err := os.Stat(device.Path())
	if err != nil {
		return 0, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, ErrCouldNotGetDeviceStat
	}

	major := uint64(stat.Rdev / 256)
	minor := uint64(stat.Rdev % 256)

	return int((major << 8) | minor), nil
}

func (l *LoopMount) Close() error {
	if l.device != nil {
		return l.device.Detach()
	}

	return nil
}
