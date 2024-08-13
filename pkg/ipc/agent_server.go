package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/loopholelabs/drafter/internal/remotes"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
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

type AgentServer struct {
	VSockPath string

	Close func()

	lis net.Listener

	closed    bool
	closeLock sync.Mutex
}

type AcceptingAgentServer struct {
	Remote remotes.AgentRemote

	Wait  func() error
	Close func() error
}

func StartAgentServer(
	vsockPath string,
	vsockPort uint32,
) (
	agentServer *AgentServer,

	err error,
) {
	agentServer = &AgentServer{
		Close: func() {},
	}

	agentServer.VSockPath = fmt.Sprintf("%s_%d", vsockPath, vsockPort)

	agentServer.lis, err = net.Listen("unix", agentServer.VSockPath)
	if err != nil {
		return nil, errors.Join(ErrCouldNotListenInAgentServer, err)
	}

	agentServer.Close = func() {
		agentServer.closeLock.Lock()
		defer agentServer.closeLock.Unlock()

		agentServer.closed = true

		// We need to remove this file first so that the client can't try to reconnect
		_ = os.Remove(agentServer.VSockPath) // We ignore errors here since the file might already have been removed, but we don't want to use `RemoveAll` cause it could remove a directory

		_ = agentServer.lis.Close() // We ignore errors here since we might interrupt a network connection
	}

	return
}

func (agentServer *AgentServer) Accept(acceptCtx context.Context, remoteCtx context.Context) (acceptingAgentServer *AcceptingAgentServer, errs error) {
	acceptingAgentServer = &AcceptingAgentServer{
		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
	}

	// We use the background context here instead of the internal context because we want to distinguish
	// between a context cancellation from the outside and getting a response
	readyCtx, cancelReadyCtx := context.WithCancel(context.Background())
	defer cancelReadyCtx()

	goroutineManager := manager.NewGoroutineManager(
		acceptCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.WaitForForegroundGoroutines()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return a `Wait()` function
	go func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-goroutineManager.GetGoroutineCtx().Done():
			agentServer.Close() // We ignore errors here since we might interrupt a network connection

		// Happy case; we've got a connection and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		// We don't do anything here because the `acceptingAgent` context handler must close in order
		case <-readyCtx.Done():
			break
		}
	}()

	conn, err := agentServer.lis.Accept()
	if err != nil {
		agentServer.closeLock.Lock()
		defer agentServer.closeLock.Unlock()

		if agentServer.closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
			if err := goroutineManager.GetGoroutineCtx().Err(); err != nil {
				panic(goroutineManager.GetGoroutineCtx().Err())
			}

			return
		}

		panic(errors.Join(ErrCouldNotAcceptAgentClient, err))
	}

	acceptingAgentServer.Close = func() error {
		agentServer.closeLock.Lock()

		agentServer.closed = true

		_ = conn.Close() // We ignore errors here since we might interrupt a network connection

		agentServer.closeLock.Unlock()

		if acceptingAgentServer.Wait != nil {
			return acceptingAgentServer.Wait()
		}

		return nil
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return a `Wait()` function.
	// We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	goroutineManager.StartBackgroundGoroutine(func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-goroutineManager.GetGoroutineCtx().Done():
			if err := acceptingAgentServer.Close(); err != nil {
				panic(errors.Join(ErrCouldNotCloseAcceptingAgentServer, err))
			}
			agentServer.Close() // We ignore errors here since we might interrupt a network connection

		// Happy case; we've got a connection and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-readyCtx.Done():
			<-remoteCtx.Done()

			if err := acceptingAgentServer.Close(); err != nil {
				panic(errors.Join(ErrCouldNotCloseAcceptingAgentServer, err))
			}
			agentServer.Close() // We ignore errors here since we might interrupt a network connection

			break
		}
	})

	registry := rpc.NewRegistry[remotes.AgentRemote, json.RawMessage](
		&struct{}{},

		remoteCtx, // This resource outlives the current scope, so we use the external context

		&rpc.RegistryHooks{
			OnClientConnect: func(remoteID string) {
				cancelReadyCtx()
			},
		},
	)

	acceptingAgentServer.Wait = sync.OnceValue(func() error {
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

			nil,
		); err != nil {
			agentServer.closeLock.Lock()
			defer agentServer.closeLock.Unlock()

			if agentServer.closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
				return remoteCtx.Err()
			}

			return errors.Join(ErrAgentClientDisconnected, ErrCouldNotLinkRegistry, err)
		}

		return nil
	})

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// any errors we get as we're polling the socket path directory are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	goroutineManager.StartBackgroundGoroutine(func() {
		if err := acceptingAgentServer.Wait(); err != nil {
			panic(err)
		}
	})

	select {
	case <-goroutineManager.GetGoroutineCtx().Done():
		if err := goroutineManager.GetGoroutineCtx().Err(); err != nil {
			panic(goroutineManager.GetGoroutineCtx().Err())
		}

		return
	case <-readyCtx.Done():
		break
	}

	found := false
	if err := registry.ForRemotes(func(remoteID string, r remotes.AgentRemote) error {
		acceptingAgentServer.Remote = r
		found = true

		return nil
	}); err != nil {
		panic(err)
	}

	if !found {
		panic(ErrNoRemoteFound)
	}

	return
}
