package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/loopholelabs/drafter/pkg/utils"
)

var (
	ErrLivenessClientAcceptFailed = errors.New("liveness client accept failed")
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
	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	handleGoroutinePanics(true, func() {
		// Cause the `Accept()` function to unblock
		<-ctx.Done()

		l.Close()
	})

	defer l.Close()

	// Don't close the connection here - we close close the listener
	if _, err := l.lis.Accept(); err != nil {
		l.closeLock.Lock()
		defer l.closeLock.Unlock()

		if l.closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
			if err := ctx.Err(); err != nil {
				panic(ctx.Err())
			}

			return
		}

		panic(errors.Join(ErrLivenessClientAcceptFailed, err))
	}

	return
}

func (l *LivenessServer) Close() {
	l.closeLock.Lock()
	defer l.closeLock.Unlock()

	l.closed = true

	// We need to remove this file first so that the client can't try to reconnect
	_ = os.Remove(l.vsockPortPath) // We ignore errors here since the file might already have been removed, but we don't want to use `RemoveAll` cause it could remove a directory

	if l.lis != nil {
		_ = l.lis.Close() // We ignore errors here since we might interrupt a network connection
	}
}
