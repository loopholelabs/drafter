package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	agentServerLocal L

	remote chan R

	listenCtx    context.Context
	listenCancel context.CancelFunc

	connFactoryShutdown func()
}

/**
 * Start an AgentRPC
 *
 *
 */
func StartAgentRPC[L AgentServerLocal, R AgentServerRemote[G], G any](log loggingtypes.Logger, vsockPath string, vsockPort uint32,
	agentServerLocal L,
) (*AgentRPC[L, R, G], error) {
	lis, err := net.Listen("unix", fmt.Sprintf("%s_%d", vsockPath, vsockPort))
	if err != nil {
		return nil, errors.Join(ErrCouldNotListenInAgentServer, err)
	}

	connFactoryShutdown := func() {
		_ = os.Remove(fmt.Sprintf("%s_%d", vsockPath, vsockPort))
		_ = lis.Close()
	}

	connFactory := func(ctx context.Context) (io.ReadWriteCloser, error) {
		return lis.Accept()
	}

	return StartAgentServer[L, R, G](log, agentServerLocal, connFactory, connFactoryShutdown)
}

/**
 * More general case of AgentServer
 *
 */
func StartAgentServer[L AgentServerLocal, R AgentServerRemote[G], G any](log loggingtypes.Logger,
	agentServerLocal L,
	connFactory func(context.Context) (io.ReadWriteCloser, error), connFactoryShutdown func(),
) (*AgentRPC[L, R, G], error) {

	listenCtx, listenCancel := context.WithCancel(context.Background())

	agentServer := &AgentRPC[L, R, G]{
		listenCtx:           listenCtx,
		listenCancel:        listenCancel,
		log:                 log,
		agentServerLocal:    agentServerLocal,
		remote:              make(chan R, 1),
		connFactoryShutdown: connFactoryShutdown,
	}

	// Start accepting connections, and keep going until listenCtx is done
	go func() {
		for {
			select {
			case <-listenCtx.Done():
				if log != nil {
					log.Debug().Msg("AgentRPC.listener shut down")
				}
				return
			default:
			}
			conn, err := connFactory(listenCtx)
			if log != nil {
				log.Debug().Err(err).Msg("AgentServer.listener accepted")
			}
			if err == nil {
				// Handle the connection, and wait until it's finished (We only handle ONE at a time)
				err = handleRPCConnection(log, listenCtx, conn, 10*time.Second, agentServer.remote, agentServer.agentServerLocal)
				if log != nil {
					log.Debug().Err(err).Msg("AgentServer.listener handle finished")
				}
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
		a.log.Info().Msg("Closing AgentRPC")
	}

	// Cancel the listenCtx
	a.listenCancel()

	a.connFactoryShutdown()

	return nil
}

/**
 * Wait and get a valid remote
 *
 */
func (a *AgentRPC[L, R, G]) GetRemote(ctx context.Context) (R, error) {
	if a.log != nil {
		a.log.Trace().Msg("AgentRPC waiting to GetRemote")
	}

	for {
		select {
		case r := <-a.remote:
			select {
			case a.remote <- r: // Put it back on the channel for another consumer
				if a.log != nil {
					a.log.Trace().Msg("AgentRPC GetRemote found remote")
				}
				return r, nil
			default:
				// We could not put it back on the channel, so there's a new one. We should loop round and get the new one.
			}
		case <-ctx.Done():
			if a.log != nil {
				a.log.Trace().Msg("AgentRPC GetRemote timed out")
			}
			return R{}, ctx.Err()
		}
	}
}

/**
 * Handle a connection
 *
 */
func handleConnection[R any](ctx context.Context, registry *rpc.Registry[R, json.RawMessage], conn io.ReadWriteCloser) error {
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

/**
 * Handle an incoming connection
 *
 */
func handleRPCConnection[L any, R any](log loggingtypes.Logger, ctx context.Context, conn io.ReadWriteCloser, readyTimeout time.Duration, remoteChan chan R, local L) error {
	// Clear remote if it's set
	select {
	case <-remoteChan:
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
		local,
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
		if log != nil {
			log.Debug().Err(err).Msg("AgentRPC handle connection finished")
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
		remoteChan <- r
		if log != nil {
			log.Debug().Msg("AgentRPC remote found and set")
		}
		found = true
		return nil
	})

	// If we found a remote, set something up to clear it at the end.
	if found {
		defer func() {
			// Clear the remote if it's set
			select {
			case <-remoteChan:
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
