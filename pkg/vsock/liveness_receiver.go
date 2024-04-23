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
	errFinished = errors.New("finished")
)

type LivenessPingReceiver struct {
	vsockPortPath string

	lis net.Listener
}

func NewLivenessPingReceiver(
	vsockPath string,
	vsockPort uint32,
) *LivenessPingReceiver {
	return &LivenessPingReceiver{
		vsockPortPath: fmt.Sprintf("%s_%d", vsockPath, vsockPort),
	}
}

func (l *LivenessPingReceiver) Open() (string, error) {
	var err error
	l.lis, err = net.Listen("unix", l.vsockPortPath)
	if err != nil {
		return "", err
	}

	return l.vsockPortPath, nil
}

func (l *LivenessPingReceiver) ReceiveAndClose(ctx context.Context) (errs []error) {
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
					errs = append(errs, e)
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

		_ = l.lis.Close() // We ignore errors here since we might interrupt a network connection
	}()

	defer l.Close()

	conn, err := l.lis.Accept()
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	return nil
}

func (l *LivenessPingReceiver) Close() {
	if l.lis != nil {
		_ = l.lis.Close()
	}

	_ = os.Remove(l.vsockPortPath)
}
