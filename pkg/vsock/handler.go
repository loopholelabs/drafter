package vsock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

type Handler struct {
	vsockPath string
	vsockPort uint32

	conn net.Conn

	wg   sync.WaitGroup
	errs chan error
}

func NewHandler(
	vsockPath string,
	vsockPort uint32,
) *Handler {
	return &Handler{
		vsockPath: vsockPath,
		vsockPort: vsockPort,

		wg:   sync.WaitGroup{},
		errs: make(chan error),
	}
}

func (s *Handler) Wait() error {
	for err := range s.errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Handler) Open(
	ctx context.Context,
	connectDeadline time.Duration,
	retryDeadline time.Duration,
) (remotes.AgentRemote, error) {
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

	connectToService := func() (bool, error) {
		var (
			errs = make(chan error)
			done = make(chan struct{})
		)

		go func() {
			var err error
			s.conn, err = net.Dial("unix", s.vsockPath)
			if err != nil {
				errs <- err

				return
			}

			if _, err = s.conn.Write([]byte(fmt.Sprintf("CONNECT %d\n", s.vsockPort))); err != nil {
				errs <- err

				return
			}

			line, err := iutils.ReadLineNoBuffer(s.conn)
			if err != nil {
				errs <- err

				return
			}

			if !strings.HasPrefix(line, "OK ") {
				errs <- ErrCouldNotConnectToVSock

				return
			}

			done <- struct{}{}
		}()

		select {
		case err := <-errs:
			if !errors.Is(err, io.EOF) {
				return false, err
			}

			return true, nil
		case <-time.After(connectDeadline):
			return true, nil

		case <-done:
			return false, nil
		}
	}

	before := time.Now()

	for {
		if time.Since(before) > retryDeadline {
			return remotes.AgentRemote{}, ErrCouldNotConnectToVSock
		}

		retry, err := connectToService()
		if err != nil {
			log.Println(err)

			return remotes.AgentRemote{}, err
		}

		if !retry {
			break
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		encoder := json.NewEncoder(s.conn)
		decoder := json.NewDecoder(s.conn)

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
			s.errs <- err

			return
		}

		close(s.errs)
	}()

	remoteID := <-ready

	var (
		remote remotes.AgentRemote
		ok     bool
	)
	// We can safely ignore the errors here, since errors are bubbled up from `cb`,
	// which can never return an error here
	_ = registry.ForRemotes(func(candidateID string, candidate remotes.AgentRemote) error {
		if candidateID == remoteID {
			remote = candidate
			ok = true
		}

		return nil
	})
	if !ok {
		return remotes.AgentRemote{}, ErrRemoteNotFound
	}

	return remote, nil
}

func (s *Handler) Close() error {
	if s.conn != nil {
		_ = s.conn.Close()
	}

	s.wg.Wait()

	return nil
}
