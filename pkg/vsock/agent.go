package vsock

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/mdlayher/vsock"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

type Agent struct {
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

func NewAgent(
	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	timeout time.Duration,

	verbose bool,
) *Agent {
	return &Agent{
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

func (s *Agent) Wait() error {
	for err := range s.errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Agent) Open(ctx context.Context) error {
	svc := utils.NewAgent(
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

func (s *Agent) Close() error {
	if s.lis != nil {
		_ = s.lis.Close()
	}

	s.wg.Wait()

	return nil
}
