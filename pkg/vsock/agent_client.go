package vsock

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	ErrAgentServerDisconnected = errors.New("agent server disconnected")
)

type local struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error
}

func (l *local) BeforeSuspend(ctx context.Context) error {
	return l.beforeSuspend(ctx)
}

func (l *local) AfterResume(ctx context.Context) error {
	return l.afterResume(ctx)
}

func StartAgentClient(
	ctx context.Context,

	vsockCID uint32,
	vsockPort uint32,

	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	registryOptions *rpc.Options,
) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // No need to use `errFinished` here because we don't pass the context to any code path that would report the cause

	conn, err := DialContext(
		ctx,

		vsockCID,
		vsockPort,
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	var closeLock sync.Mutex
	closed := false

	wg.Add(1)
	go func() {
		defer wg.Done()

		<-ctx.Done()

		closeLock.Lock()
		defer closeLock.Unlock()

		_ = conn.Close() // We ignore errors here since we might interrupt a network connection

		closed = true
	}()

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		&local{
			beforeSuspend: beforeSuspend,
			afterResume:   afterResume,
		},

		ctx,

		registryOptions,
	)

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
	); err != nil {
		closeLock.Lock()
		defer closeLock.Unlock()

		if closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
			return ctx.Err()
		}

		return errors.Join(ErrAgentServerDisconnected, err)
	}

	return nil
}
