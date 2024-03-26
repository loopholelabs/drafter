package vsock

import (
	"io"

	"golang.org/x/sys/unix"
)

type Listener struct {
	fd int
}

func Listen(cid uint32, port uint32, backlog int) (*Listener, error) {
	l, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	socketAddr := &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}

	if err := unix.Bind(l, socketAddr); err != nil {
		_ = unix.Close(l)
		return nil, err
	}

	if err := unix.Listen(l, backlog); err != nil {
		_ = unix.Close(l)
		return nil, err
	}

	return &Listener{fd: l}, nil
}

func (l *Listener) Accept() (io.ReadWriteCloser, error) {
	fd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}

	return &Conn{fd: fd}, nil
}

func (l *Listener) Close() error {
	return unix.Close(l.fd)
}
