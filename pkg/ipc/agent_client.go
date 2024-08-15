package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/internal/vsock"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrAgentServerDisconnected = errors.New("agent server disconnected")
	ErrCouldNotDialVSock       = errors.New("could not dial VSock")
	ErrCouldNotMarshalJSON     = errors.New("could not marshal JSON")
	ErrCouldNotUnmarshalJSON   = errors.New("could not unmarshal JSON")
	ErrAgentContextCancelled   = errors.New("agent context cancelled")
)

type AgentClient struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error
}

func NewAgentClient(
	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,
) *AgentClient {
	return &AgentClient{
		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,
	}
}

func (l *AgentClient) BeforeSuspend(ctx context.Context) error {
	return l.beforeSuspend(ctx)
}

func (l *AgentClient) AfterResume(ctx context.Context) error {
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

	agentClient *AgentClient,
) (connectedAgentClient *ConnectedAgentClient, errs error) {
	connectedAgentClient = &ConnectedAgentClient{
		Wait: func() error {
			return nil
		},
		Close: func() {},
	}

	goroutineManager := manager.NewGoroutineManager(
		dialCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	conn, err := vsock.DialContext(
		goroutineManager.Context(),

		vsockCID,
		vsockPort,
	)
	if err != nil {
		panic(errors.Join(ErrCouldNotDialVSock, err))
	}

	var closeLock sync.Mutex
	closed := false

	connectedAgentClient.Close = func() {
		closeLock.Lock()
		defer closeLock.Unlock()

		closed = true

		_ = conn.Close() // We ignore errors here since we might interrupt a network connection
	}

	ready := make(chan any)
	// This goroutine will not leak on function return because it selects on `goroutineManager.Context().Done()`
	// internally and we return a wait function
	goroutineManager.StartBackgroundGoroutine(func(ctx context.Context) {
		select {
		// Failure case; something failed and the goroutineManager.Context() was cancelled before we got a connection
		case <-ctx.Done():
			connectedAgentClient.Close() // We ignore errors here since we might interrupt a network connection

		// Happy case; we've got a connection and we want to wait with closing the agent's connections until the ready channel is closed.
		case <-ready:
			<-remoteCtx.Done()

			connectedAgentClient.Close() // We ignore errors here since we might interrupt a network connection

			break
		}
	})

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		agentClient,

		remoteCtx, // This resource outlives the current scope, so we use the external context

		&rpc.RegistryHooks{
			OnClientConnect: func(remoteID string) {
				close(ready)
			},
		},
	)

	connectedAgentClient.Wait = sync.OnceValue(func() error {
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
					return nil, errors.Join(ErrCouldNotMarshalJSON, err)
				}

				return json.RawMessage(b), nil
			},
			func(data json.RawMessage, v any) error {
				if err := json.Unmarshal([]byte(data), v); err != nil {
					return errors.Join(ErrCouldNotUnmarshalJSON, err)
				}

				return nil
			},

			nil,
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

	// It is safe to start a background goroutine here since we return a wait function
	// Despite returning a wait function, we still need to start this goroutine however so that any errors
	// we get as we're waiting for a connection are caught
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := connectedAgentClient.Wait(); err != nil {
			panic(errors.Join(ErrAgentContextCancelled, err))
		}
	})

	select {
	case <-goroutineManager.Context().Done():
		if err := goroutineManager.Context().Err(); err != nil {
			panic(errors.Join(ErrAgentContextCancelled, err))
		}

		return
	case <-ready:
		break
	}

	return
}
