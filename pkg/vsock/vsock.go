package vsock

import "golang.org/x/sys/unix"

type Conn struct {
	fd int
}

func NewConn(cid uint32, port uint32) (*Conn, error) {
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

type Listener struct {
	fd int
}

func NewListener(cid uint32, port uint32, backlog int) (*Listener, error) {
	l, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	socketAddr := &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}

	err = unix.Bind(l, socketAddr)
	if err != nil {
		_ = unix.Close(l)
		return nil, err
	}

	err = unix.Listen(l, backlog)
	if err != nil {
		_ = unix.Close(l)
		return nil, err
	}

	return &Listener{fd: l}, nil
}

func (l *Listener) Accept() (*Conn, error) {
	fd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}

	return &Conn{fd: fd}, nil
}

func (l *Listener) Close() error {
	return unix.Close(l.fd)
}
