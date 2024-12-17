package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
)

// Callbacks
type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllMigrationsCompleted func()
}

/**
 * MigrateTo migrates to a remote VM.
 *
 *
 */
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

	// Start a migration to the protocol. This will send all schema info etc
	err := migratablePeer.Dg.StartMigrationTo(pro)
	if err != nil {
		return err
	}

	// Progress handler
	// TODO: We should probably expose this in hooks
	pHandler := func(p map[string]*migrator.MigrationProgress) {
		totalSize := 0
		totalDone := 0
		for _, prog := range p {
			totalSize += (prog.TotalBlocks * prog.BlockSize)
			totalDone += (prog.ReadyBlocks * prog.BlockSize)
		}

		perc := float64(0.0)
		if totalSize > 0 {
			perc = float64(totalDone) * 100 / float64(totalSize)
		}
		// Report overall migration progress
		fmt.Printf("# Overall migration Progress # (%d / %d) %.1f%%\n", totalDone, totalSize, perc)

		// Report individual devices
		for name, prog := range p {
			fmt.Printf(" [%s] Progress Migrated Blocks (%d/%d)  %d ready\n", name, prog.MigratedBlocks, prog.TotalBlocks, prog.ReadyBlocks)
		}
	}

	// Do the main migration of the data...
	err = migratablePeer.Dg.MigrateAll(concurrency, pHandler)
	if err != nil {
		return err
	}

	// This manages the status of the VM - if it's suspended or not.
	vmState := NewVMStateMgr(goroutineManager.Context(),
		migratablePeer.SuspendAndCloseAgentServer,
		suspendTimeout,
		migratablePeer.resumedRunner.Msync,
		hooks.OnBeforeSuspend,
		hooks.OnAfterSuspend,
	)

	dirtyDevices := make(map[string]*DeviceStatus, 0)
	for _, d := range devices {
		dirtyDevices[d.Name] = &DeviceStatus{
			CycleThrottle:  d.CycleThrottle,
			MinCycles:      d.MinCycles,
			MaxCycles:      d.MaxCycles,
			MaxDirtyBlocks: d.MaxDirtyBlocks,
		}
	}

	// When we are ready to transfer authority, we send a single Custom Event here.
	authTransfer := func() error {
		return migratablePeer.Dg.SendCustomData([]byte{byte(registry.EventCustomTransferAuthority)})
	}

	dm := NewDirtyManager(vmState, dirtyDevices, authTransfer)

	err = migratablePeer.Dg.MigrateDirty(&devicegroup.MigrateDirtyHooks{
		PreGetDirty:      dm.PreGetDirty,
		PostGetDirty:     dm.PostGetDirty,
		PostMigrateDirty: dm.PostMigrateDirty,
		Completed:        func(name string) {},
	})
	if err != nil {
		return err
	}

	// Send Silo completion events for the devices. This will trigger any S3 sync behaviour etc.
	err = migratablePeer.Dg.Completed()
	if err != nil {
		return err
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}
	return
}
