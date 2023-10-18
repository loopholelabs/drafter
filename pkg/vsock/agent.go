package vsock

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/services"
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
}

func NewAgent(
	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	timeout time.Duration,
) *Agent {
	return &Agent{
		vsockCID:  vsockCID,
		vsockPort: vsockPort,

		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,

		timeout: timeout,

		wg:   sync.WaitGroup{},
		errs: make(chan error),
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
	svc := services.NewAgent(
		s.beforeSuspend,
		s.afterResume,
	)

	registry := rpc.NewRegistry(
		svc,
		struct{}{},

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

				if err := registry.LinkStream(
					json.NewEncoder(conn).Encode,
					json.NewDecoder(conn).Decode,

					json.Marshal,
					json.Unmarshal,
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
