package vsock

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

var (
	ErrLivenessClientAcceptFailed = errors.New("liveness client accept failed")
)

var (
	errFinished = errors.New("finished")
)

type LivenessServer struct {
	vsockPortPath string

	lis net.Listener

	closeLock sync.Mutex
	closed    bool
}

func NewLivenessServer(
	vsockPath string,
	vsockPort uint32,
) *LivenessServer {
	return &LivenessServer{
		vsockPortPath: fmt.Sprintf("%s_%d", vsockPath, vsockPort),
	}
}

func (l *LivenessServer) Open() (string, error) {
	var err error
	l.lis, err = net.Listen("unix", l.vsockPortPath)
	if err != nil {
		return "", err
	}

	return l.vsockPortPath, nil
}

func (l *LivenessServer) ReceiveAndClose(ctx context.Context) (errs error) {
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		// Cause the `Accept()` function to unblock
		<-ctx.Done()

		l.Close()
	}()

	defer l.Close()

	// Don't close the connection here - we close close the listener
	if _, err := l.lis.Accept(); err != nil {
		l.closeLock.Lock()
		defer l.closeLock.Unlock()

		if l.closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
			panic(ctx.Err())
		}

		panic(errors.Join(ErrLivenessClientAcceptFailed, err))
	}

	return
}

func (l *LivenessServer) Close() {
	l.closeLock.Lock()
	defer l.closeLock.Unlock()

	// We need to remove this file first so that the client can't try to reconnect
	_ = os.Remove(l.vsockPortPath) // We ignore errors here since the file might already have been removed, but we don't want to use `RemoveAll` cause it could remove a directory

	if l.lis != nil {
		_ = l.lis.Close() // We ignore errors here since we might interrupt a network connection
	}

	l.closed = true
}
