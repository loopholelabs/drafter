package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

var (
	ErrCouldNotAcceptLivenessClient   = errors.New("could not accept liveness client")
	ErrCouldNotListenInLivenessServer = errors.New("could not listen in liveness server")
	ErrLivenessServerContextCancelled = errors.New("liveness server context cancelled")
)

type LivenessServer struct {
	vsockPortPath string
	lis           net.Listener
	closeLock     sync.Mutex
	closed        bool
}

func NewLivenessServer(vsockPath string, vsockPort uint32) *LivenessServer {
	return &LivenessServer{
		vsockPortPath: fmt.Sprintf("%s_%d", vsockPath, vsockPort),
	}
}

func (l *LivenessServer) Open() (string, error) {
	var err error
	l.lis, err = net.Listen("unix", l.vsockPortPath)
	if err != nil {
		return "", errors.Join(ErrCouldNotListenInLivenessServer, err)
	}
	return l.vsockPortPath, nil
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

func (l *LivenessServer) ReceiveAndClose(ctx context.Context) (errs error) {
	defer l.Close()

	connErr := make(chan error, 1)

	go func() {
		// Don't close the connection here - we close the listener
		_, err := l.lis.Accept()
		connErr <- err
	}()

	select {
	case <-ctx.Done():
		return errors.Join(ErrLivenessServerContextCancelled, ctx.Err())
	case e := <-connErr:
		if e == nil {
			return nil // It all worked...
		}

		l.closeLock.Lock()
		defer l.closeLock.Unlock()

		if l.closed && errors.Is(e, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
			return nil // We closed the connection, report no error
		}
		return errors.Join(ErrCouldNotAcceptLivenessClient, e)
	}
}
