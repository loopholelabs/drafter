package vsock

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

var (
	ErrVSockSocketCreationFailed = errors.New("could not create VSOCK socket")
	ErrVSockDialContextCancelled = errors.New("VSock dial context cancelled")
	ErrVSockConnectFailed        = errors.New("could not connect to VSOCK socket")
)

func DialContext(ctx context.Context, cid uint32, port uint32) (c io.ReadWriteCloser, errs error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, errors.Join(ErrVSockSocketCreationFailed, err)
	}

	connErr := make(chan error, 1)

	// Try to connect in a goroutine...
	go func() {
		err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
		connErr <- err
	}()

	select {
	case <-ctx.Done():
		errShutdown := unix.Shutdown(fd, unix.SHUT_RDWR)
		errClose := unix.Close(fd)
		return nil, errors.Join(ErrCouldNotCloseVSockConn, errShutdown, errClose)
	case e := <-connErr:
		if e != nil {
			return nil, errors.Join(ErrVSockConnectFailed, err)
		}
		return &conn{fd}, nil
	}
}
