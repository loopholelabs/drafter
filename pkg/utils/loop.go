package utils

import (
	"github.com/freddierice/go-losetup/v2"
)

type Loop struct {
	file   string
	device *losetup.Device
}

func NewLoop(file string) *Loop {
	return &Loop{file: file}
}

func (l *Loop) Open() (string, error) {
	device, err := losetup.Attach(l.file, 0, false)
	if err != nil {
		return "", err
	}

	l.device = &device

	return l.device.Path(), nil
}

func (l *Loop) Close() error {
	if l.device != nil {
		return l.device.Detach()
	}

	return nil
}
