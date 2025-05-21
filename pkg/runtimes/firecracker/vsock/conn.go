package vsock

import (
	"errors"
	"io"
	"net"

	"golang.org/x/sys/unix"
)

var (
	ErrVSockConnectionBadFileDescriptor = errors.New("bad file descriptor in VSock connection")
	ErrCouldNotShutdownVSockConnection  = errors.New("could not shut down VSock connection")
	ErrCouldNotCloseVSockConn           = errors.New("could not close VSock connection")
)

type conn struct {
	fd int
}

func (c *conn) Read(b []byte) (int, error) {
	n, err := unix.Read(c.fd, b)
	if err != nil {
		if errors.Is(err, unix.EBADF) { // Report bad file descriptor errors as closed errors
			err = errors.Join(net.ErrClosed, ErrVSockConnectionBadFileDescriptor, err)
		}
		return n, err
	}

	if n == 0 {
		return 0, io.EOF
	}

	return n, nil
}

func (v *conn) Write(b []byte) (int, error) {
	n, err := unix.Write(v.fd, b)
	if err != nil {
		if errors.Is(err, unix.EBADF) { // Report bad file descriptor errors as closed errors
			err = errors.Join(net.ErrClosed, ErrVSockConnectionBadFileDescriptor, err)
		}
		return n, err
	}

	return n, nil
}

func (v *conn) Close() error {
	if err := unix.Shutdown(v.fd, unix.SHUT_RDWR); err != nil {
		// Always close the file descriptor even if shutdown fails
		if e := unix.Close(v.fd); e != nil {
			err = errors.Join(ErrCouldNotCloseVSockConn, ErrCouldNotShutdownVSockConnection, e, err)
		} else {
			err = errors.Join(ErrCouldNotShutdownVSockConnection, err)
		}
		return err
	}

	if err := unix.Close(v.fd); err != nil {
		return errors.Join(ErrCouldNotCloseVSockConn, err)
	}

	return nil
}
