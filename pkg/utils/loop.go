package utils

import (
	"errors"

	"github.com/freddierice/go-losetup/v2"
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get device stat")
	ErrCouldNotAttachDevice  = errors.New("could not attach device")
	ErrCouldNotDetachDevice  = errors.New("could not detach device")
)

type LoopMount struct {
	file   string
	device *losetup.Device
}

func NewLoopMount(file string) *LoopMount {
	return &LoopMount{file: file}
}

func (l *LoopMount) Open() (string, error) {
	device, err := losetup.Attach(l.file, 0, false)
	if err != nil {
		return "", errors.Join(ErrCouldNotAttachDevice, err)
	}

	l.device = &device

	return device.Path(), nil
}

func (l *LoopMount) Close() error {
	if l.device != nil {
		if err := l.device.Detach(); err != nil {
			return errors.Join(ErrCouldNotDetachDevice, err)
		}
	}

	return nil
}
