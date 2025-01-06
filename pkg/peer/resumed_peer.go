package peer

import (
	"context"
	"io"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

type ResumedPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Dg            *devicegroup.DeviceGroup
	Remote        R
	Wait          func() error
	Close         func() error
	resumedRunner *runner.ResumedRunner[L, R, G]
}

func (resumedPeer *ResumedPeer[L, R, G]) SuspendAndCloseAgentServer(ctx context.Context, resumeTimeout time.Duration) error {
	return resumedPeer.resumedRunner.SuspendAndCloseAgentServer(
		ctx,

		resumeTimeout,
	)
}

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

	// Log checksums...
	/*
		names := migratablePeer.Dg.GetAllNames()
		for _, n := range names {
			di := migratablePeer.Dg.GetDeviceInformationByName(n)

			dirty := di.DirtyRemote.MeasureDirty()

			// Read straight from device...
			fp, err := os.Open(fmt.Sprintf("/dev/%s", di.Exp.Device()))
			if err != nil {
				panic(err)
			}

			size := di.Prov.Size()
			buffer := make([]byte, size)
			fp.ReadAt(buffer, 0)
			hash := sha256.Sum256(buffer)
			log.Printf("DATA[%s] %d hash %x dirty %d\n", n, size, hash, dirty)

			fp.Close()
		}
	*/
	if hooks.OnAllMigrationsCompleted != nil {
		hooks.OnAllMigrationsCompleted()
	}
	return nil
}
