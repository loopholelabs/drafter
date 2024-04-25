package vsock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/remotes"
	"github.com/loopholelabs/drafter/pkg/utils"
	iutils "github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrCouldNotConnectToVSock = errors.New("could not connect to VSock")
	ErrRemoteNotFound         = errors.New("remote not found")
)

type AgentHandler struct {
	Remote remotes.AgentRemote
	Wait   func() error
	Close  func() error
}

func CreateNewAgentHandler(
	ctx context.Context,

	vsockPath string,
	vsockPort uint32,

	connectDeadline time.Duration,
	retryDeadline time.Duration,
) (agentHandler *AgentHandler, errs error) {
	agentHandler = &AgentHandler{}

	var errsLock sync.Mutex

	internalCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(errFinished)

	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), errFinished)) {
					errs = errors.Join(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	ready := make(chan string)

	registry := rpc.NewRegistry[remotes.AgentRemote, json.RawMessage](
		struct{}{},

		ctx,

		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				ready <- remoteID
			},
		},
	)

	var conn net.Conn
	connectToService := func() (bool, error) {
		var (
			err  error
			done = make(chan struct{})
		)
		go func() {
			defer close(done)

			conn, err = net.Dial("unix", vsockPath)
			if err != nil {
				return
			}

			if _, err = conn.Write([]byte(fmt.Sprintf("CONNECT %d\n", vsockPort))); err != nil {
				return
			}

			var line string
			line, err = iutils.ReadLineNoBuffer(conn)
			if err != nil {
				return
			}

			if !strings.HasPrefix(line, "OK ") {
				err = ErrCouldNotConnectToVSock

				return
			}
		}()

		select {
		case <-done:
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return false, err
				}

				return true, nil
			}

			return false, nil

		case <-time.After(connectDeadline):
			return true, nil

		case <-internalCtx.Done():
			return false, context.Cause(internalCtx)
		}
	}

	before := time.Now()
	for {
		if time.Since(before) > retryDeadline {
			panic(ErrCouldNotConnectToVSock)
		}

		retry, err := connectToService()
		if err != nil {
			panic(err)
		}

		if !retry {
			break
		}
	}

	var wg sync.WaitGroup
	agentHandler.Wait = func() error {
		wg.Wait()

		return errs
	}

	agentHandler.Close = func() error {
		if err := conn.Close(); err != nil {
			return err
		}

		return agentHandler.Wait()
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here since we return the process's wait method
	// We still need to `defer handleGoroutinePanic()()` here however so that we catch any errors during this call
	go func() {
		defer handleGoroutinePanic()()

		<-ctx.Done() // We use ctx, not internalCtx here since this resource outlives the function call

		if err := agentHandler.Close(); err != nil {
			panic(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

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
		); err != nil && !utils.IsClosedErr(err) && !strings.HasSuffix(err.Error(), "use of closed network connection") {
			panic(err)
		}
	}()

	var remoteID string
	select {
	case remoteID = <-ready:
		break

	case <-internalCtx.Done():
		panic(context.Cause(internalCtx))
	}

	found := false
	// We can safely ignore the errors here, since errors are bubbled up from `cb`,
	// which can never return an error here
	_ = registry.ForRemotes(func(candidateID string, candidate remotes.AgentRemote) error {
		if candidateID == remoteID {
			agentHandler.Remote = candidate
			found = true
		}

		return nil
	})
	if !found {
		panic(ErrRemoteNotFound)
	}

	return
}
