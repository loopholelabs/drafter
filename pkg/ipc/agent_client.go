package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/runtimes/firecracker/vsock"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrAgentServerDisconnected = errors.New("agent server disconnected")
	ErrCouldNotDialVSock       = errors.New("could not dial VSock")
	ErrAgentContextCancelled   = errors.New("agent context cancelled")
)

// The RPCs the agent server can call on this client
// See https://github.com/pojntfx/panrpc/tree/main?tab=readme-ov-file#5-calling-the-clients-rpcs-from-the-server
type AgentClientLocal[G any] struct {
	GuestService G

	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error
}

// The RPCs this client can call on the agent server
// See https://github.com/pojntfx/panrpc/tree/main?tab=readme-ov-file#4-calling-the-servers-rpcs-from-the-client
type AgentClientRemote any

func NewAgentClient[G any](guestService G, beforeSuspend func(ctx context.Context) error, afterResume func(ctx context.Context) error) *AgentClientLocal[G] {
	return &AgentClientLocal[G]{
		GuestService:  guestService,
		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,
	}
}

func (l *AgentClientLocal[G]) BeforeSuspend(ctx context.Context) error {
	return l.beforeSuspend(ctx)
}

func (l *AgentClientLocal[G]) AfterResume(ctx context.Context) error {
	return l.afterResume(ctx)
}

type ConnectedAgentClient[L *AgentClientLocal[G], R AgentClientRemote, G any] struct {
	log              loggingtypes.Logger
	remote           chan R
	listenCtx        context.Context
	listenCancel     context.CancelFunc
	agentClientLocal L
}

func (a *ConnectedAgentClient[L, R, G]) Close() error {
	if a.log != nil {
		a.log.Info().Msg("Closing AgentClient")
	}

	a.listenCancel()
	return nil
}

func StartAgentClient[L *AgentClientLocal[G], R AgentClientRemote, G any](
	log loggingtypes.Logger,
	dialCtx context.Context,
	remoteCtx context.Context,
	vsockCID uint32,
	vsockPort uint32,
	agentClientLocal L,
) (connectedAgentClient *ConnectedAgentClient[L, R, G], errs error) {
	listenCtx, listenCancel := context.WithCancel(context.Background())

	connectedAgentClient = &ConnectedAgentClient[L, R, G]{
		log:              log,
		listenCtx:        listenCtx,
		listenCancel:     listenCancel,
		remote:           make(chan R, 1),
		agentClientLocal: agentClientLocal,
	}

	go func() {
		for {
			select {
			case <-listenCtx.Done():
				if log != nil {
					log.Debug().Msg("AgentClient.connector shut down")
				}
				return
			default:
			}
			conn, err := vsock.DialContext(dialCtx, vsockCID, vsockPort)
			if log != nil {
				log.Debug().Err(err).Msg("AgentClient.connector accepted")
			}
			if err == nil {
				err = connectedAgentClient.handle(listenCtx, conn, 10*time.Second)
				if log != nil {
					log.Debug().Err(err).Msg("AgentClient.connector handle finished")
				}
			}
		}
	}()

	return connectedAgentClient, nil
}

func (a *ConnectedAgentClient[L, R, G]) handle(ctx context.Context, conn io.ReadWriteCloser, readyTimeout time.Duration) error {
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

	registry := rpc.NewRegistry[R, json.RawMessage](
		a.agentClientLocal,
		&rpc.RegistryHooks{
			OnClientConnect: func(remoteID string) {
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
			a.log.Debug().Msg("AgentClient remote found and set")
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
