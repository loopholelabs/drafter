package mount

import (
	"github.com/freddierice/go-losetup/v2"
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
		return "", err
	}

	l.device = &device

	return l.device.Path(), nil
}

func (l *LoopMount) Close() error {
	if l.device != nil {
		return l.device.Detach()
	}

	return nil
}
