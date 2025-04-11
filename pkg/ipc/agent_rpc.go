package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

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

type AgentRPC[L AgentServerLocal, R AgentServerRemote[G], G any] struct {
	log              loggingtypes.Logger
	VSockPath        string
	agentServerLocal L

	remote chan R

	lis          net.Listener
	listenCtx    context.Context
	listenCancel context.CancelFunc
}

/**
 * Start an AgentRPC
 *
 *
 */
func StartAgentRPC[L AgentServerLocal, R AgentServerRemote[G], G any](log loggingtypes.Logger, vsockPath string, vsockPort uint32,
	agentServerLocal L) (*AgentRPC[L, R, G], error) {

	listenCtx, listenCancel := context.WithCancel(context.Background())

	var err error
	agentServer := &AgentRPC[L, R, G]{
		listenCtx:        listenCtx,
		listenCancel:     listenCancel,
		log:              log,
		agentServerLocal: agentServerLocal,
		VSockPath:        fmt.Sprintf("%s_%d", vsockPath, vsockPort),
		remote:           make(chan R, 1),
	}

	agentServer.lis, err = net.Listen("unix", agentServer.VSockPath)
	if err != nil {
		return nil, errors.Join(ErrCouldNotListenInAgentServer, err)
	}

	// Start accepting connections
	go func() {
		select {
		case <-listenCtx.Done():
			if log != nil {
				log.Debug().Str("VSockPath", agentServer.VSockPath).Msg("AgentRPC.listener shut down")
			}
			return
		default:
		}
		conn, err := agentServer.lis.Accept()
		if log != nil {
			log.Debug().Str("VSockPath", agentServer.VSockPath).Err(err).Msg("AgentServer.listener accepted")
		}
		if err == nil {
			// Handle the connection, and wait until it's finished (We only handle ONE at a time)
			err = agentServer.handle(listenCtx, conn, 10*time.Second)
			if log != nil {
				log.Debug().Str("VSockPath", agentServer.VSockPath).Err(err).Msg("AgentServer.listener handle finished")
			}
		}
	}()

	return agentServer, nil
}

/**
 * Close the AgentRPC
 *
 */
func (a *AgentRPC[L, R, G]) Close() error {
	if a.log != nil {
		a.log.Info().Str("VSockPath", a.VSockPath).Msg("Closing AgentRPC")
	}

	// Cancel the listenCtx
	a.listenCancel()

	// We need to remove this file first so that the client can't try to reconnect
	os.Remove(a.VSockPath) // We ignore errors here since the file might already have been removed, but we don't want to use `RemoveAll` cause it could remove a directory
	a.lis.Close()          // We ignore errors here since we might interrupt a network connection

	return nil
}

/**
 * Handle an incoming connection
 *
 */
func (a AgentRPC[L, R, G]) handle(ctx context.Context, conn net.Conn, readyTimeout time.Duration) error {
	// Clear remote if it's set
	select {
	case <-a.remote:
	default:
	}

	ready := make(chan bool)

	// Cancel context when we return
	linkCtx, linkCancel := context.WithCancel(ctx)
	defer linkCancel()

	// Setup a timeout to wait for the RPC to be ready
	readyCtx, readyCancel := context.WithTimeout(linkCtx, readyTimeout)
	defer readyCancel()

	// Setup registry
	registry := rpc.NewRegistry[R, json.RawMessage](
		a.agentServerLocal,
		&rpc.RegistryHooks{OnClientConnect: func(remoteID string) {
			// Signal that we're ready
			close(ready)
		},
		},
	)

	var connectionWg sync.WaitGroup

	// Handle the connection here.
	connectionWg.Add(1)
	go func() {
		defer linkCancel()
		err := handleConnection[R](linkCtx, registry, conn)
		if a.log != nil {
			a.log.Debug().Err(err).Msg("AgentRPC handle connection finished")
		}
		connectionWg.Done()
	}()

	select {
	case <-readyCtx.Done(): // Timeout waiting for RPC ready
		return readyCtx.Err()
	case <-ready: // Ready to use connection
		break
	}

	found := false
	err := registry.ForRemotes(func(remoteID string, r R) error {
		a.remote <- r
		if a.log != nil {
			a.log.Debug().Msg("AgentRPC remote found and set")
		}
		found = true
		return nil
	})

	// If we found a remote, set something up to clear it at the end.
	if found {
		defer func() {
			// Clear the remote if it's set
			select {
			case <-a.remote:
			default:
			}
		}()
	}

	if err != nil {
		return err
	}

	if !found {
		return ErrNoRemoteFound
	}

	// Now wait until this connection is done with
	connectionWg.Wait()
	return nil
}

/**
 * Wait and get a valid remote
 *
 */
func (a *AgentRPC[L, R, G]) GetRemote(ctx context.Context) (R, error) {
	if a.log != nil {
		a.log.Trace().Str("VSockPath", a.VSockPath).Msg("AgentRPC waiting to GetRemote")
	}

	select {
	case r := <-a.remote:
		a.remote <- r // Put it back on the channel for another consumer
		if a.log != nil {
			a.log.Trace().Str("VSockPath", a.VSockPath).Msg("AgentRPC GetRemote found remote")
		}
		return r, nil
	case <-ctx.Done():
		if a.log != nil {
			a.log.Trace().Str("VSockPath", a.VSockPath).Msg("AgentRPC GetRemote timed out")
		}
		return R{}, ctx.Err()
	}
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
