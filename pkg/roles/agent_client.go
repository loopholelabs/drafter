package roles

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/internal/vsock"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrAgentServerDisconnected = errors.New("agent server disconnected")
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
	connectedAgentClient = &ConnectedAgentClient{}

	// We use the background context here instead of the internal context because we want to distinguish
	// between a context cancellation from the outside and getting a response
	readyCtx, cancelReadyCtx := context.WithCancel(context.Background())
	defer cancelReadyCtx()

	internalCtx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		dialCtx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	conn, err := vsock.DialContext(
		internalCtx,

		vsockCID,
		vsockPort,
	)
	if err != nil {
		panic(err)
	}

	var closeLock sync.Mutex
	closed := false

	connectedAgentClient.Close = func() {
		closeLock.Lock()
		defer closeLock.Unlock()

		closed = true

		_ = conn.Close() // We ignore errors here since we might interrupt a network connection
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return a `Wait()` function.
	// We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	handleGoroutinePanics(false, func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-internalCtx.Done():
			connectedAgentClient.Close() // We ignore errors here since we might interrupt a network connection

		// Happy case; we've got a connection and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-readyCtx.Done():
			<-remoteCtx.Done()

			connectedAgentClient.Close() // We ignore errors here since we might interrupt a network connection

			break
		}
	})

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		agentClient,

		remoteCtx, // This resource outlives the current scope, so we use the external context

		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				cancelReadyCtx()
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
		if err := connectedAgentClient.Wait(); err != nil {
			panic(err)
		}
	})

	select {
	case <-internalCtx.Done():
		if err := internalCtx.Err(); err != nil {
			panic(internalCtx.Err())
		}

		return
	case <-readyCtx.Done():
		break
	}

	return
}
