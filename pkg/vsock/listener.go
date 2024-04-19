package vsock

import (
	"io"

	"golang.org/x/sys/unix"
)

type Listener struct {
	fd int
}

func Listen(cid uint32, port uint32, backlog int) (*Listener, error) {
	lis, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	if err := unix.Bind(lis, &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}); err != nil {
		return nil, unix.Close(lis)
	}

	if err := unix.Listen(lis, backlog); err != nil {
		return nil, unix.Close(lis)
	}

	return &Listener{fd: lis}, nil
}

func (l *Listener) Accept() (io.ReadWriteCloser, error) {
	fd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}

	return &conn{fd}, nil
}

func (l *Listener) Close() error {
	return unix.Close(l.fd)
}
