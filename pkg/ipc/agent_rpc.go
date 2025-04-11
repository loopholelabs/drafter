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
 * Start an AgentServer
 *
 */
func StartAgentServer[L AgentServerLocal, R AgentServerRemote[G], G any](log loggingtypes.Logger, vsockPath string, vsockPort uint32,
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
		if err == nil {
			// Handle the connection, and wait until it's finished (We only handle ONE at a time)
			err = agentServer.handle(listenCtx, conn)
			if log != nil {
				log.Debug().Str("VSockPath", agentServer.VSockPath).Err(err).Msg("AgentServer.handle finished")
			}
		}
	}()

	return agentServer, nil
}

func (a AgentRPC[L, R, G]) handle(ctx context.Context, conn net.Conn) error {
	ready := make(chan bool)

	// Setup the connection, and use it...
	linkCtx, linkCancel := context.WithCancel(ctx)
	defer linkCancel()

	readyCtx, readyCancel := context.WithTimeout(linkCtx, 10*time.Second)
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
			a.log.Info().Err(err).Msg("Handle connection finished")
		}
		connectionWg.Done()
	}()

	select {
	case <-readyCtx.Done(): // Timeout waiting for RPC ready
		return readyCtx.Err()
	case <-ready:
		break
	}

	if a.log != nil {
		a.log.Debug().Msg("RPC ready")
	}

	found := false
	err := registry.ForRemotes(func(remoteID string, r R) error {
		select {
		case <-a.remote:
		default:
		}

		a.remote <- r
		if a.log != nil {
			a.log.Debug().Msg("RPC set remote")
		}
		found = true
		return nil
	})

	if err != nil {
		return err
	}

	if !found {
		return ErrNoRemoteFound
	}

	// Now wait until it's done
	connectionWg.Wait()

	<-a.remote

	return nil
}

/**
 * Close the AgentServer
 *
 */
func (a *AgentRPC[L, R, G]) Close() error {
	if a.log != nil {
		a.log.Info().Str("VSockPath", a.VSockPath).Msg("Closing AgentServer")
	}

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
 * Wait and get a valid remote
 *
 */
func (a *AgentRPC[L, R, G]) GetRemote(ctx context.Context) (R, error) {
	if a.log != nil {
		a.log.Debug().Str("VSockPath", a.VSockPath).Msg("GetRemote")
	}

	select {
	case r := <-a.remote:
		a.remote <- r
		if a.log != nil {
			a.log.Debug().Str("VSockPath", a.VSockPath).Msg("GetRemote found remote")
		}
		return r, nil
	case <-ctx.Done():
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
