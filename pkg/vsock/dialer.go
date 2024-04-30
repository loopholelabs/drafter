package vsock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

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
	var errsLock sync.Mutex

	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errFinished)

	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
					errs = errors.Join(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		<-ctx.Done()

		// Non-happy path; context was cancelled before `connect()` completed
		if c == nil {
			if err := unix.Shutdown(fd, unix.SHUT_RD); err != nil {
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
	}()

	if err = unix.Connect(fd, &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}); err != nil {
		panic(err)
	}

	c = &conn{fd}

	return
}
