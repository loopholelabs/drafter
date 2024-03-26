package services

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"log"
	"sync"

	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

type vsock struct {
	fd int
}

func (v *vsock) Close() error {
	return unix.Close(v.fd)
}

func (v *vsock) Read(b []byte) (int, error) {
	return unix.Read(v.fd, b)
}

func (v *vsock) Write(b []byte) (int, error) {
	return unix.Write(v.fd, b)
}

type Agent struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error

	verbose bool
}

func NewAgent(
	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	verbose bool,
) *Agent {
	return &Agent{
		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,

		verbose: verbose,
	}
}

func (a *Agent) BeforeSuspend(ctx context.Context) error {
	if a.verbose {
		log.Println("BeforeSuspend()")
	}

	return a.beforeSuspend(ctx)
}

func (a *Agent) AfterResume(ctx context.Context) error {
	if a.verbose {
		log.Println("AfterResume()")
	}

	return a.afterResume(ctx)
}

type AgentServer struct {
	vsockCID  uint32
	vsockPort uint32

	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error

	lis int

	wg   sync.WaitGroup
	errs chan error

	verbose bool
}

func NewAgentServer(
	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	verbose bool,
) *AgentServer {
	return &AgentServer{
		vsockCID:  vsockCID,
		vsockPort: vsockPort,

		lis: -1,

		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,

		wg:   sync.WaitGroup{},
		errs: make(chan error),

		verbose: verbose,
	}
}

func (s *AgentServer) Wait() error {
	for err := range s.errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *AgentServer) Open(ctx context.Context) error {
	svc := NewAgent(
		s.beforeSuspend,
		s.afterResume,

		s.verbose,
	)

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		svc,

		ctx,

		nil,
	)

	var err error
	s.lis, err = unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}

	socketAddr := &unix.SockaddrVM{
		CID:  s.vsockCID,
		Port: s.vsockPort,
	}

	err = unix.Bind(s.lis, socketAddr)
	if err != nil {
		return err
	}

	err = unix.Listen(s.lis, 2)
	if err != nil {

	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			connFd, _, err := unix.Accept(s.lis)
			if err != nil {
				if !utils.IsClosedErr(err) {
					s.errs <- err
					return
				}
				break
			}

			conn := &vsock{
				fd: connFd,
			}

			go func() {
				defer conn.Close()

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
				); err != nil && !utils.IsClosedErr(err) {
					s.errs <- err

					return
				}
			}()
		}

		close(s.errs)
	}()

	return nil
}

func (s *AgentServer) Close() error {
	if s.lis > -1 {
		_ = unix.Close(s.lis)
	}

	s.wg.Wait()

	return nil
}
