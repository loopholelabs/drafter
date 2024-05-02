package vsock

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrAgentServerDisconnected = errors.New("agent server disconnected")
)

type local struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error
}

func (l *local) BeforeSuspend(ctx context.Context) error {
	return l.beforeSuspend(ctx)
}

func (l *local) AfterResume(ctx context.Context) error {
	return l.afterResume(ctx)
}

type ConnectedAgentClient struct {
	Wait  func() error
	Close func()
}

func StartAgentClient(
	dialCtx context.Context,
	remoteCtx context.Context,

	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,
) (connectedAgent *ConnectedAgentClient, errs error) {
	connectedAgent = &ConnectedAgentClient{}

	internalCtx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		dialCtx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	// We use the background context here instead of the internal context because we want to distinguish
	// between a context cancellation from the outside and getting a response
	readyCtx, cancelReadyCtx := context.WithCancel(dialCtx)
	defer cancelReadyCtx()

	conn, err := DialContext(
		internalCtx,

		vsockCID,
		vsockPort,
	)
	if err != nil {
		panic(err)
	}

	var closeLock sync.Mutex
	closed := false

	connectedAgent.Close = func() {
		closeLock.Lock()
		defer closeLock.Unlock()

		_ = conn.Close() // We ignore errors here since we might interrupt a network connection

		closed = true
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return a `Wait()` function.
	// We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	handleGoroutinePanics(false, func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-internalCtx.Done():
			connectedAgent.Close() // We ignore errors here since we might interrupt a network connection

		// Happy case; we've got a connection and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-readyCtx.Done():
			<-remoteCtx.Done()

			connectedAgent.Close() // We ignore errors here since we might interrupt a network connection

			break
		}
	})

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		&local{
			beforeSuspend: beforeSuspend,
			afterResume:   afterResume,
		},

		remoteCtx, // This resource outlives the current scope, so we use the external context

		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				cancelReadyCtx()
			},
		},
	)

	connectedAgent.Wait = sync.OnceValue(func() error {
		encoder := json.NewEncoder(conn)
		decoder := json.NewDecoder(conn)

		if err := registry.LinkStream(
			func(v rpc.Message[json.RawMessage]) error {
				return encoder.Encode(v)
			},
			func(v *rpc.Message[json.RawMessage]) error {
				return decoder.Decode(v)
			},

			func(v any) (json.RawMessage, error) {
				b, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}

				return json.RawMessage(b), nil
			},
			func(data json.RawMessage, v any) error {
				return json.Unmarshal([]byte(data), v)
			},
		); err != nil {
			closeLock.Lock()
			defer closeLock.Unlock()

			if closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
				return remoteCtx.Err()
			}

			return errors.Join(ErrAgentServerDisconnected, err)
		}

		return nil
	})

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// any errors we get as we're polling the socket path directory are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	handleGoroutinePanics(false, func() {
		if err := connectedAgent.Wait(); err != nil {
			panic(err)
		}
	})

	select {
	case <-internalCtx.Done():
		panic(internalCtx.Err())
	case <-readyCtx.Done():
		break
	}

	return
}
