package vsock

import (
	"context"
	"errors"
	"io"

	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"golang.org/x/sys/unix"
)

var (
	ErrVSockSocketCreationFailed = errors.New("could not create VSOCK socket")
	ErrVSockDialContextCancelled = errors.New("VSock dial context cancelled")
	ErrVSockConnectFailed        = errors.New("could not connect to VSOCK socket")
)

func DialContext(
	ctx context.Context,

	cid uint32,
	port uint32,
) (c io.ReadWriteCloser, errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(errors.Join(ErrVSockSocketCreationFailed, err))
	}

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		<-goroutineManager.Context().Done()

		// Non-happy path; context was cancelled before `connect()` completed
		if c == nil {
			if err := unix.Shutdown(fd, unix.SHUT_RDWR); err != nil {
				// Always close the file descriptor even if shutdown fails
				if e := unix.Close(fd); e != nil {
					err = errors.Join(ErrCouldNotShutdownVSockConnection, ErrCouldNotCloseVSockConn, e, err)
				} else {
					err = errors.Join(ErrCouldNotShutdownVSockConnection, err)
				}

				panic(err)
			}

			if err := unix.Close(fd); err != nil {
				panic(errors.Join(ErrCouldNotCloseVSockConn, err))
			}

			if err := goroutineManager.Context().Err(); err != nil {
				panic(errors.Join(ErrVSockDialContextCancelled, err))
			}

			return
		}
	})

	if err = unix.Connect(fd, &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}); err != nil {
		panic(errors.Join(ErrVSockConnectFailed, err))
	}

	c = &conn{fd}

	return
}
