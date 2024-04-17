package vsock

import (
	"io"

	"golang.org/x/sys/unix"
)

type conn struct {
	fd int
}

func (c *conn) Read(b []byte) (int, error) {
	n, err := unix.Read(c.fd, b)
	if err != nil {
		return 0, err
	}

	if n == 0 {
		return 0, io.EOF
	}

	return n, nil
}

func (v *conn) Write(b []byte) (int, error) {
	return unix.Write(v.fd, b)
}

func (v *conn) Close() error {
	return unix.Close(v.fd)
}
