package vsock

import (
	"golang.org/x/sys/unix"
)

type Conn struct {
	fd int
}

func Dial(cid uint32, port uint32) (*Conn, error) {
	socket, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	socketAddr := &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}

	if err = unix.Connect(socket, socketAddr); err != nil {
		return nil, err
	}

	return &Conn{fd: socket}, nil
}

func (v *Conn) Close() error {
	return unix.Close(v.fd)
}

func (v *Conn) Read(b []byte) (int, error) {
	return unix.Read(v.fd, b)
}

func (v *Conn) Write(b []byte) (int, error) {
	return unix.Write(v.fd, b)
}
