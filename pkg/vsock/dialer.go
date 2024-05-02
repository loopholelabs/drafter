package vsock

import (
	"context"
	"errors"
	"io"

	"github.com/loopholelabs/drafter/pkg/utils"
	"golang.org/x/sys/unix"
)

const (
	CIDHost  = 2
	CIDGuest = 3
)

func DialContext(
	ctx context.Context,

	cid uint32,
	port uint32,
) (c io.ReadWriteCloser, errs error) {
	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(err)
	}

	handleGoroutinePanics(true, func() {
		<-ctx.Done()

		// Non-happy path; context was cancelled before `connect()` completed
		if c == nil {
			if err := unix.Shutdown(fd, unix.SHUT_RDWR); err != nil {
				// Always close the file descriptor even if shutdown fails
				if e := unix.Close(fd); e != nil {
					err = errors.Join(e, err)
				}

				panic(err)
			}

			if err := unix.Close(fd); err != nil {
				panic(err)
			}

			panic(ctx.Err())
		}
	})

	if err = unix.Connect(fd, &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}); err != nil {
		panic(err)
	}

	c = &conn{fd}

	return
}
