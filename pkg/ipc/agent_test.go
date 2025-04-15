package ipc

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loopholelabs/logging"
	"github.com/stretchr/testify/assert"
)

type PipeBidirectional struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func NewPipes() (*PipeBidirectional, *PipeBidirectional) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()

	return &PipeBidirectional{r1, w2}, &PipeBidirectional{r2, w1}
}

func (p *PipeBidirectional) Read(b []byte) (int, error) {
	return p.r.Read(b)
}

func (p *PipeBidirectional) Write(b []byte) (int, error) {
	return p.w.Write(b)
}

func (p *PipeBidirectional) Close() error {
	e1 := p.r.Close()
	e2 := p.w.Close()
	if e1 != nil || e2 != nil {
		return fmt.Errorf("e1: %w e2: %w", e1, e2)
	}
	return nil
}

func TestAgent(t *testing.T) {
	log := logging.New(logging.Zerolog, "test", os.Stdout)

	con1, con2 := NewPipes()

	connFactory1 := func(ctx context.Context) (io.ReadWriteCloser, error) {
		return con1, nil
	}
	connFactory2 := func(ctx context.Context) (io.ReadWriteCloser, error) {
		return con2, nil
	}
	connFactoryShutdown := func() {
		fmt.Printf("Shutdown here...\n")
	}

	// Start a "server"
	agent, err := StartAgentServer[struct{}, AgentServerRemote[struct{}]](
		log, struct{}{}, connFactory1, connFactoryShutdown,
	)

	assert.NoError(t, err)

	defer agent.Close()

	countBeforeSuspend := uint64(0)
	countAfterResume := uint64(0)

	// Start a "client"
	beforeSuspendFn := func(ctx context.Context) error {
		fmt.Printf("BeforeSuspend\n")
		atomic.AddUint64(&countBeforeSuspend, 1)
		return nil
	}
	afterResumeFn := func(ctx context.Context) error {
		fmt.Printf("AfterResume\n")
		atomic.AddUint64(&countAfterResume, 1)
		return nil
	}
	agentClient := NewAgentClient[struct{}](struct{}{}, beforeSuspendFn, afterResumeFn)

	client, err := StartAgentClient[*AgentClientLocal[struct{}], struct{}](log, connFactory2, agentClient)
	assert.NoError(t, err)

	defer client.Close()

	// Try doing RPC calls...

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := agent.GetRemote(ctx)
	assert.NoError(t, err)

	beforeSuependCtx, beforeSuspendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer beforeSuspendCancel()

	fmt.Printf("Calling BeforeSuspend...\n")
	err = r.BeforeSuspend(beforeSuependCtx) // FIXME: SOMETIMES HANGS!!!
	fmt.Printf("Called BeforeSuspend...\n")
	assert.NoError(t, err)

	//	err = r.AfterResume(context.Background())
	//	assert.NoError(t, err)

	fmt.Printf("Done calls\n")

	assert.Equal(t, uint64(1), atomic.LoadUint64(&countBeforeSuspend))
	//	assert.Equal(t, uint64(1), atomic.LoadUint64(&countAfterResume))

	fmt.Printf("End of test\n")
}
