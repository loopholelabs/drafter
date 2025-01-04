package peer

import (
	"context"
	"io"
	"time"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

// Callbacks
type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllMigrationsCompleted func()
	OnProgress               func(p map[string]*migrator.MigrationProgress)
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
) error {

	// This manages the status of the VM - if it's suspended or not.
	vmState := NewVMStateMgr(ctx,
		migratablePeer.SuspendAndCloseAgentServer,
		suspendTimeout,
		migratablePeer.resumedRunner.Msync,
		hooks.OnBeforeSuspend,
		hooks.OnAfterSuspend,
	)

	err := migrateToPipe(ctx, readers, writers, migratablePeer.Dg, concurrency, hooks.OnProgress, vmState, devices)

	if err != nil {
		return err
	}

	if hooks.OnAllMigrationsCompleted != nil {
		hooks.OnAllMigrationsCompleted()
	}
	return nil
}
