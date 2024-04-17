package vsock

import (
	"io"

	"golang.org/x/sys/unix"
)

func Dial(cid uint32, port uint32) (io.ReadWriteCloser, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	if err = unix.Connect(fd, &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}); err != nil {
		return nil, err
	}

	return &conn{fd}, nil
}
