package vsock

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/services"
	iutils "github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/dudirekta/pkg/rpc"
	"github.com/pojntfx/r3map/pkg/utils"
)

var (
	ErrCouldNotConnectToVSock = errors.New("could not connect to VSock")
	ErrPeerNotFound           = errors.New("peer not found")
)

type Handler struct {
	vsockPath string
	vsockPort uint32

	timeout time.Duration

	conn net.Conn

	wg   sync.WaitGroup
	errs chan error
}

func NewHandler(
	vsockPath string,
	vsockPort uint32,

	timeout time.Duration,
) *Handler {
	return &Handler{
		vsockPath: vsockPath,
		vsockPort: vsockPort,

		timeout: timeout,

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

func (s *Handler) Open(ctx context.Context) (services.AgentRemote, error) {
	ready := make(chan string)

	registry := rpc.NewRegistry(
		struct{}{},
		services.AgentRemote{},

		s.timeout,
		ctx,
		&rpc.Options{
			ResponseBufferLen: rpc.DefaultResponseBufferLen,
			OnClientConnect: func(remoteID string) {
				ready <- remoteID
			},
		},
	)

	var err error
	s.conn, err = net.Dial("unix", s.vsockPath)
	if err != nil {
		return services.AgentRemote{}, err
	}

	if _, err := s.conn.Write([]byte(fmt.Sprintf("CONNECT %d\n", s.vsockPort))); err != nil {
		return services.AgentRemote{}, err
	}

	line, err := iutils.ReadLineNoBuffer(s.conn)
	if err != nil {
		return services.AgentRemote{}, err
	}

	if !strings.HasPrefix(line, "OK ") {
		return services.AgentRemote{}, ErrCouldNotConnectToVSock
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		if err := registry.Link(s.conn); err != nil && !utils.IsClosedErr(err) && !strings.HasSuffix(err.Error(), "use of closed network connection") {
			s.errs <- err

			return
		}

		close(s.errs)
	}()

	remoteID := <-ready
	peer, ok := registry.Peers()[remoteID]
	if !ok {
		return services.AgentRemote{}, ErrPeerNotFound
	}

	return peer, nil
}

func (s *Handler) Close() error {
	if s.conn != nil {
		_ = s.conn.Close()
	}

	s.wg.Wait()

	return nil
}
