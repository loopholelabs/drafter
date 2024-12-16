package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
)

type MigrateToHooks struct {
	OnBeforeSuspend func()
	OnAfterSuspend  func()

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

	pro := protocol.NewRW(
		goroutineManager.Context(),
		readers,
		writers,
		nil,
	)

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(errors.Join(registry.ErrCouldNotHandleProtocol, err))
		}
	})

	var (
		suspendedVMLock sync.Mutex
		suspendedVM     bool
	)

	suspendedVMCh := make(chan struct{})

	checkSuspendedVM := func() bool {
		suspendedVMLock.Lock()
		defer suspendedVMLock.Unlock()
		return suspendedVM
	}

	suspendAndMsyncVM := sync.OnceValue(func() error {
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
	})

	siloDevices := make([]*MigrateToStage, 0)

	names := migratablePeer.Dg.GetAllNames()
	fmt.Printf("Silo devices are %v\n", names)

	for _, input := range names {
		for _, device := range devices {
			if device.Name == input {
				siloDevices = append(siloDevices, &MigrateToStage{
					Name:             device.Name,
					Remote:           true,
					VolatilityExpiry: 30 * time.Minute, // TODO...

					MaxDirtyBlocks: device.MaxDirtyBlocks,
					MinCycles:      device.MinCycles,
					MaxCycles:      device.MaxCycles,
					CycleThrottle:  device.CycleThrottle,
				})
				break
			}
		}
	}

	vmState := &VMStateManager{
		checkSuspendedVM:  checkSuspendedVM,
		suspendAndMsyncVM: suspendAndMsyncVM,
		suspendedVMCh:     suspendedVMCh,
		MSync:             migratablePeer.resumedRunner.Msync,
	}

	SiloMigrateTo(migratablePeer.Dg, siloDevices, concurrency, goroutineManager, pro, hooks, vmState)

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
