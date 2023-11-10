package services

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/mdlayher/vsock"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

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

	timeout time.Duration

	lis *vsock.Listener

	wg   sync.WaitGroup
	errs chan error

	verbose bool
}

func NewAgentServer(
	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	timeout time.Duration,

	verbose bool,
) *AgentServer {
	return &AgentServer{
		vsockCID:  vsockCID,
		vsockPort: vsockPort,

		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,

		timeout: timeout,

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

		s.timeout,
		ctx,
		nil,
	)

	var err error
	s.lis, err = vsock.ListenContextID(s.vsockCID, s.vsockPort, nil)
	if err != nil {
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			conn, err := s.lis.Accept()
			if err != nil {
				if !utils.IsClosedErr(err) {
					s.errs <- err

					return
				}

				break
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
	if s.lis != nil {
		_ = s.lis.Close()
	}

	s.wg.Wait()

	return nil
}
