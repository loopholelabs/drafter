package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrNoRemoteFound                     = errors.New("no remote found")
	ErrAgentClientDisconnected           = errors.New("agent client disconnected")
	ErrCouldNotAcceptAgentClient         = errors.New("could not accept agent client")
	ErrCouldNotListenInAgentServer       = errors.New("could not listen in agent server")
	ErrCouldNotCloseAcceptingAgentServer = errors.New("could not close accepting agent server")
	ErrCouldNotLinkRegistry              = errors.New("could not link registry")
)

// The RPCs the agent client can call on this server
// See https://github.com/pojntfx/panrpc/tree/main?tab=readme-ov-file#5-calling-the-clients-rpcs-from-the-server
type AgentServerLocal any

// The RPCs this server can call on the agent client
// See https://github.com/pojntfx/panrpc/tree/main?tab=readme-ov-file#4-calling-the-servers-rpcs-from-the-client
type AgentServerRemote[G any] struct {
	GuestService G

	BeforeSuspend func(ctx context.Context) error
	AfterResume   func(ctx context.Context) error
}

type AgentServer[L AgentServerLocal, R AgentServerRemote[G], G any] struct {
	log              loggingtypes.Logger
	VSockPath        string
	closed           bool
	closeLock        sync.Mutex
	agentServerLocal L

	lis           net.Listener
	listenCtx     context.Context
	listenCancel  context.CancelFunc
	connections   chan net.Conn
	connectionErr chan error
}

/**
 * Close the AgentServer
 *
 */
func (a *AgentServer[L, R, G]) Close() error {
	if a.log != nil {
		a.log.Info().Str("VSockPath", a.VSockPath).Msg("Closing AgentServer")
	}
	a.closeLock.Lock()
	defer a.closeLock.Unlock()
	a.closed = true

	a.listenCancel()

	// We need to remove this file first so that the client can't try to reconnect
	err := os.Remove(a.VSockPath) // We ignore errors here since the file might already have been removed, but we don't want to use `RemoveAll` cause it could remove a directory
	if err != nil {
		if a.log != nil {
			a.log.Debug().Err(err).Str("VSockPath", a.VSockPath).Msg("error removing vsockpath")
		}
	}

	err = a.lis.Close() // We ignore errors here since we might interrupt a network connection
	if err != nil {
		if a.log != nil {
			a.log.Debug().Err(err).Str("VSockPath", a.VSockPath).Msg("error closing listener")
		}
	}
	return nil
}

/**
 * Start an AgentServer
 *
 */
func StartAgentServer[L AgentServerLocal, R AgentServerRemote[G], G any](log loggingtypes.Logger, vsockPath string, vsockPort uint32,
	agentServerLocal L) (*AgentServer[L, R, G], error) {

	listenCtx, listenCancel := context.WithCancel(context.Background())

	backlog := 16

	var err error
	agentServer := &AgentServer[L, R, G]{
		listenCtx:        listenCtx,
		listenCancel:     listenCancel,
		log:              log,
		agentServerLocal: agentServerLocal,
		VSockPath:        fmt.Sprintf("%s_%d", vsockPath, vsockPort),
		connections:      make(chan net.Conn, backlog),
		connectionErr:    make(chan error, backlog),
	}

	agentServer.lis, err = net.Listen("unix", agentServer.VSockPath)
	if err != nil {
		return nil, errors.Join(ErrCouldNotListenInAgentServer, err)
	}

	if log != nil {
		log.Info().Str("VSockPath", agentServer.VSockPath).Msg("Created an AgentServer")
	}

	// Start accepting here so we *know* we are always accepting connections
	go func() {
		select {
		case <-listenCtx.Done():
			if log != nil {
				log.Debug().Str("VSockPath", agentServer.VSockPath).Msg("AgentServer.listener shut down")
			}
			return
		default:
		}
		conn, err := agentServer.lis.Accept()
		if log != nil {
			log.Debug().Str("VSockPath", agentServer.VSockPath).Err(err).Msg("AgentServer.listener accepted")
		}
		if err != nil {
			agentServer.connectionErr <- err
		} else {
			agentServer.connections <- conn
		}
	}()

	return agentServer, nil
}

/**
 * Handle a connection
 *
 */
func handleConnection[R any](ctx context.Context, registry *rpc.Registry[R, json.RawMessage], conn net.Conn) error {
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	encodeFn := func(v rpc.Message[json.RawMessage]) error {
		return encoder.Encode(v)
	}
	decodeFn := func(v *rpc.Message[json.RawMessage]) error {
		return decoder.Decode(v)
	}
	marshalFn := func(v any) (json.RawMessage, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
	unmarshalFn := func(data json.RawMessage, v any) error {
		return json.Unmarshal([]byte(data), v)
	}

	return registry.LinkStream(ctx, encodeFn, decodeFn, marshalFn, unmarshalFn, nil)
}

// FIXME: Tidy up under here...

type AgentServerAcceptHooks[R AgentServerRemote[G], G any] struct {
	OnAfterRegistrySetup func(forRemotes func(cb func(remoteID string, remote R) error) error) error
}

type AgentConnection[L AgentServerLocal, R AgentServerRemote[G], G any] struct {
	agentServer     *AgentServer[L, R, G]
	Remote          R
	connErr         chan error
	connectionErr   error
	connectionWg    sync.WaitGroup
	connectionReady chan bool

	linkCtx    context.Context
	linkCancel context.CancelCauseFunc
	stoppedErr error

	Wait func() error
}

func (ac *AgentConnection[L, R, G]) Close() error {
	if ac.agentServer.log != nil {
		ac.agentServer.log.Info().Str("VSockPath", ac.agentServer.VSockPath).Msg("AgentConnection.Close()")
	}

	ac.agentServer.closeLock.Lock()
	ac.agentServer.closed = true
	ac.linkCancel(ac.stoppedErr)
	ac.agentServer.closeLock.Unlock()
	return ac.Wait()
}

/**
 * Accept a connection, and handle it
 *
 */
func (agentServer *AgentServer[L, R, G]) Accept(acceptCtx context.Context, remoteCtx context.Context, hooks AgentServerAcceptHooks[R, G],
) (agentConnection *AgentConnection[L, R, G], errs error) {

	agentConnection = &AgentConnection[L, R, G]{
		connectionReady: make(chan bool, 1),
		agentServer:     agentServer,
		connErr:         make(chan error, 1),
		Wait: func() error {
			return nil
		},
	}

	if agentServer.log != nil {
		agentServer.log.Info().Str("VSockPath", agentServer.VSockPath).Msg("AgentServer.Accept Accepting connection")
	}

	var conn net.Conn
	select {
	case conn = <-agentServer.connections:
	case err := <-agentServer.connectionErr:
		agentServer.closeLock.Lock()
		defer agentServer.closeLock.Unlock()
		if agentServer.closed && errors.Is(err, net.ErrClosed) {
			return nil, nil
		}
		return nil, errors.Join(ErrCouldNotAcceptAgentClient, err)
	}

	if agentServer.log != nil {
		agentServer.log.Info().Str("VSockPath", agentServer.VSockPath).Msg("AgentServer.Accept Accepted connection")
	}

	agentConnection.linkCtx, agentConnection.linkCancel = context.WithCancelCause(remoteCtx) // This resource outlives the current scope, so we use the external context

	goroutineManager := manager.NewGoroutineManager(
		acceptCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	agentConnection.stoppedErr = goroutineManager.GetErrGoroutineStopped()

	// It is safe to start a background goroutine here since we return a wait function
	// Despite returning a wait function, we still need to start this goroutine however so that any errors
	// we get as we're waiting for a connection are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	goroutineManager.StartBackgroundGoroutine(func(ctx context.Context) {
		select {
		// Failure case; something failed and the goroutineManager.Context() was cancelled before we got a connection
		case <-ctx.Done():

			// Happy case; we've got a connection and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
			select {
			case <-agentConnection.connectionReady:
				<-remoteCtx.Done() // Wait here...
			default:
			}

			if err := agentConnection.Close(); err != nil {
				panic(errors.Join(ErrCouldNotCloseAcceptingAgentServer, err))
			}
			agentServer.Close() // We ignore errors here since we might interrupt a network connection
		}
	})

	registry := rpc.NewRegistry[R, json.RawMessage](
		agentServer.agentServerLocal,
		&rpc.RegistryHooks{OnClientConnect: func(remoteID string) {
			// Signal that we're ready
			close(agentConnection.connectionReady)
		},
		},
	)

	if hooks.OnAfterRegistrySetup != nil {
		hooks.OnAfterRegistrySetup(registry.ForRemotes)
	}

	// Handle the connection here.
	agentConnection.connectionWg.Add(1)
	go func() {
		defer agentConnection.linkCancel(nil)
		err := handleConnection[R](agentConnection.linkCtx, registry, conn)
		agentConnection.connErr <- err
		agentConnection.connectionErr = err
		agentConnection.connectionWg.Done()
	}()

	agentConnection.Wait = sync.OnceValue(func() error {
		// We don't `defer conn.Close` here since Firecracker handles resetting active VSock connections for us
		err := <-agentConnection.connErr

		if err != nil {
			agentServer.closeLock.Lock()
			defer agentServer.closeLock.Unlock()

			// Don't treat closed errors as errors if we closed the connection
			if !(agentServer.closed && errors.Is(err, context.Canceled) && errors.Is(context.Cause(goroutineManager.Context()), goroutineManager.GetErrGoroutineStopped())) {
				return errors.Join(ErrAgentClientDisconnected, ErrCouldNotLinkRegistry, err)
			}
			return remoteCtx.Err()
		}
		return nil
	})

	// It is safe to start a background goroutine here since we return a wait function
	// Despite returning a wait function, we still need to start this goroutine however so that any errors
	// we get as we're waiting for a connection are caught
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := agentConnection.Wait(); err != nil {
			panic(err)
		}
	})

	select {
	case <-goroutineManager.Context().Done():
		agentServer.Close()
		return nil, goroutineManager.Context().Err()
	case <-agentConnection.connectionReady:
		break
	}

	found := false
	err := registry.ForRemotes(func(remoteID string, r R) error {
		agentConnection.Remote = r
		found = true
		return nil
	})

	if err != nil {
		return nil, err
	}

	if !found {
		return nil, ErrNoRemoteFound
	}

	return
}
