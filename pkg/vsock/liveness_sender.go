package vsock

import (
	"golang.org/x/sys/unix"
)

const (
	CIDHost  = 2
	CIDGuest = 3
)

func SendLivenessPing(
	vsockCID uint32,
	vsockPort uint32,
) error {
	socket, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}

	socketAddr := &unix.SockaddrVM{
		CID:  vsockCID,
		Port: vsockPort,
	}

	err = unix.Connect(socket, socketAddr)
	if err != nil {
		return err
	}

	_ = unix.Close(socket)
	return nil
}
