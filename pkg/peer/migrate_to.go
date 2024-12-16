package peer

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
)

type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

func (migratablePeer *ResumedPeer[L, R, G]) MigrateTo(
	ctx context.Context,
	devices []mounter.MigrateToDevice,
	suspendTimeout time.Duration,
	concurrency int,
	readers []io.Reader,
	writers []io.Writer,
	hooks MigrateToHooks,
) (errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	protocolCtx := goroutineManager.Context()

	// Create a protocol for use by Silo
	pro := protocol.NewRW(protocolCtx, readers, writers, nil)

	// Start a goroutine to do the protocol Handle()
	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		err := pro.Handle()
		if err != nil && !errors.Is(err, io.EOF) {
			panic(errors.Join(registry.ErrCouldNotHandleProtocol, err))
		}
	})

	// This lot manages vmState. Should probabloy pull it out
	var suspendedVMLock sync.Mutex
	var suspendedVM bool
	suspendedVMCh := make(chan struct{})

	vmState := &VMStateManager{
		checkSuspendedVM: func() bool {
			suspendedVMLock.Lock()
			defer suspendedVMLock.Unlock()
			return suspendedVM
		},
		suspendAndMsyncVM: sync.OnceValue(func() error {
			if hook := hooks.OnBeforeSuspend; hook != nil {
				hook()
			}

			if err := migratablePeer.SuspendAndCloseAgentServer(goroutineManager.Context(), suspendTimeout); err != nil {
				return errors.Join(ErrCouldNotSuspendAndCloseAgentServer, err)
			}

			if err := migratablePeer.resumedRunner.Msync(goroutineManager.Context()); err != nil {
				return errors.Join(ErrCouldNotMsyncRunner, err)
			}

			if hook := hooks.OnAfterSuspend; hook != nil {
				hook()
			}

			suspendedVMLock.Lock()
			suspendedVM = true
			suspendedVMLock.Unlock()

			close(suspendedVMCh) // We can safely close() this channel since the caller only runs once/is `sync.OnceValue`d
			return nil
		}),
		suspendedVMCh: suspendedVMCh,
		MSync:         migratablePeer.resumedRunner.Msync,
	}

	SiloMigrateTo(migratablePeer.Dg, devices, concurrency, goroutineManager, pro, hooks, vmState)

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
