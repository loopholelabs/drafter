package peer

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

type Peer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	VMPath string
	VMPid  int

	Wait  func() error
	Close func() error

	hypervisorCtx context.Context

	runner *runner.Runner[L, R, G]
}

func StartPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	hypervisorCtx context.Context,
	rescueCtx context.Context,

	hypervisorConfiguration snapshotter.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	peer *Peer[L, R, G],

	errs error,
) {
	peer = &Peer[L, R, G]{
		hypervisorCtx: hypervisorCtx,

		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
	}

	goroutineManager := manager.NewGoroutineManager(
		hypervisorCtx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	var err error
	peer.runner, err = runner.StartRunner[L, R](
		hypervisorCtx,
		rescueCtx,

		hypervisorConfiguration,

		stateName,
		memoryName,
	)

	// We set both of these even if we return an error since we need to have a way to wait for rescue operations to complete
	peer.Wait = peer.runner.Wait
	peer.Close = func() error {
		if err := peer.runner.Close(); err != nil {
			return err
		}

		return peer.Wait()
	}

	if err != nil {
		panic(errors.Join(ErrCouldNotStartRunner, err))
	}

	peer.VMPath = peer.runner.VMPath
	peer.VMPid = peer.runner.VMPid

	// We don't track this because we return the wait function
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := peer.runner.Wait(); err != nil {
			panic(err)
		}
	})

	return
}

func (peer *Peer[L, R, G]) MigrateFrom(
	ctx context.Context,
	devices []MigrateFromDevice,
	readers []io.Reader,
	writers []io.Writer,
	hooks mounter.MigrateFromHooks,
) (
	migratedPeer *MigratedPeer[L, R, G],
	errs error,
) {

	migratedPeer = &MigratedPeer[L, R, G]{
		Wait: func() error {
			return nil
		},

		devices: devices,
		runner:  peer.runner,
	}

	migratedPeer.Close = func() (errs error) {
		// We have to close the runner before we close the devices
		if err := peer.runner.Close(); err != nil {
			return err
		}

		// Close any Silo devices
		migratedPeer.DgLock.Lock()
		if migratedPeer.Dg != nil {
			err := migratedPeer.Dg.CloseAll()
			if err != nil {
				migratedPeer.DgLock.Unlock()
				return err
			}
		}
		migratedPeer.DgLock.Unlock()
		return nil
	}

	// Migrate the devices from a protocol
	if len(readers) > 0 && len(writers) > 0 {
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		// TODO: This schema tweak function should be exposed / passed in
		tweak := func(index int, name string, schema string) string {
			s := strings.ReplaceAll(schema, "instance-0", "instance-1")
			return string(s)
		}

		dg, err := migrateFromPipe(nil, nil, migratedPeer.runner.VMPath, protocolCtx, readers, writers, tweak)
		if err != nil {
			return nil, err
		}

		migratedPeer.Wait = sync.OnceValue(func() error {
			defer cancelProtocolCtx()

			err := dg.WaitForCompletion()
			if err != nil {
				return err
			}

			// Save dg for future migrations.
			migratedPeer.DgLock.Lock()
			migratedPeer.Dg = dg
			migratedPeer.DgLock.Unlock()
			return nil
		})
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		dg, err := migrateFromFS(nil, nil, migratedPeer.runner.VMPath, devices)
		if err != nil {
			return nil, err
		}

		// Save dg for later usage, when we want to migrate from here etc
		migratedPeer.DgLock.Lock()
		migratedPeer.Dg = dg
		migratedPeer.DgLock.Unlock()

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	return
}
