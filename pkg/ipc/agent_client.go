package ipc

import (
	"context"
	"errors"
	"time"

	"github.com/loopholelabs/drafter/pkg/runtimes/firecracker/vsock"
	loggingtypes "github.com/loopholelabs/logging/types"
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

type AgentClient[L *AgentClientLocal[G], R AgentClientRemote, G any] struct {
	log              loggingtypes.Logger
	remote           chan R
	listenCtx        context.Context
	listenCancel     context.CancelFunc
	agentClientLocal L
}

func (a *AgentClient[L, R, G]) Close() error {
	if a.log != nil {
		a.log.Info().Msg("Closing AgentClient")
	}

	a.listenCancel()
	return nil
}

func StartAgentClient[L *AgentClientLocal[G], R AgentClientRemote, G any](log loggingtypes.Logger,
	dialCtx context.Context, remoteCtx context.Context,
	vsockCID uint32, vsockPort uint32, agentClientLocal L) (*AgentClient[L, R, G], error) {
	listenCtx, listenCancel := context.WithCancel(context.Background())

	connectedAgentClient := &AgentClient[L, R, G]{
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
				err = handleRPCConnection(log, listenCtx, conn, 10*time.Second, connectedAgentClient.remote, connectedAgentClient.agentClientLocal)
				if log != nil {
					log.Debug().Err(err).Msg("AgentClient.connector handle finished")
				}
			}
		}
	}()

	return connectedAgentClient, nil
}
