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
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
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

	// Start a migration to the protocol...
	err := migratablePeer.Dg.StartMigrationTo(pro)
	if err != nil {
		return err
	}

	// TODO: Get this to call hooks
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
		fmt.Printf("# Migration Progress # (%d / %d) %d%%\n", totalDone, totalSize, perc)

		for name, prog := range p {
			fmt.Printf(" [%s] Progress (%d/%d)\n", name, prog.ReadyBlocks, prog.TotalBlocks)
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

	// Try new way...
	dirtyDevices := make(map[string]*DeviceStatus, 0)
	for _, d := range devices {
		dirtyDevices[d.Name] = &DeviceStatus{
			CycleThrottle:  d.CycleThrottle,
			MinCycles:      d.MinCycles,
			MaxCycles:      d.MaxCycles,
			MaxDirtyBlocks: d.MaxDirtyBlocks,
		}
	}

	authTransfer := func() {
		fmt.Printf("# auth transfer\n")
		// For now, do it as usual...
		names := migratablePeer.Dg.GetAllNames()
		for _, n := range names {
			di := migratablePeer.Dg.GetDeviceInformationByName(n)
			err := di.To.SendEvent(&packets.Event{
				Type:       packets.EventCustom,
				CustomType: byte(registry.EventCustomTransferAuthority),
			})
			if err != nil {
				panic(errors.Join(mounter.ErrCouldNotSendTransferAuthorityEvent, err))
			}
		}
		//		migratablePeer.Dg.SendCustomData([]byte("auth transfer"))
	}

	dm := NewDirtyManager(vmState, dirtyDevices, authTransfer)

	err = migratablePeer.Dg.MigrateDirty(&devicegroup.MigrateDirtyHooks{
		PreGetDirty:      dm.PreGetDirty,
		PostGetDirty:     dm.PostGetDirty,
		PostMigrateDirty: dm.PostMigrateDirty,
		Completed: func(name string) {
			fmt.Printf("MigrateDirty done %s\n", name)
		},
	})
	/*
		err = SiloMigrateDirtyTo(migratablePeer.Dg, devices, concurrency, goroutineManager, pro, hooks, vmState)
		if err != nil {
			return err
		}
	*/
	err = migratablePeer.Dg.Completed()
	if err != nil {
		return err
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
