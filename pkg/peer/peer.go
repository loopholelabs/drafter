package peer

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
)

type Peer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	VMPath string
	VMPid  int

	hypervisorCtx context.Context

	runner *runner.Runner[L, R, G]

	alreadyClosed bool
	alreadyWaited bool
}

func (p Peer[L, R, G]) Close() error {
	if p.alreadyClosed {
		fmt.Printf("FIXME: Peer.Close called multiple times\n")
		return nil
	}
	p.alreadyClosed = true

	if p.runner != nil {
		err := p.runner.Close()
		if err != nil {
			return err
		}
		return p.runner.Wait()
	}
	return nil
}

func (p Peer[L, R, G]) Wait() error {
	if p.alreadyWaited {
		fmt.Printf("FIXME: Peer.Wait called multiple times\n")
		return nil
	}
	p.alreadyWaited = true

	if p.runner != nil {
		return p.runner.Wait()
	}
	return nil
}

func StartPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any](
	hypervisorCtx context.Context,
	rescueCtx context.Context,

	hypervisorConfiguration snapshotter.HypervisorConfiguration,

	stateName string,
	memoryName string,
) (*Peer[L, R, G], error) {
	peer := &Peer[L, R, G]{
		hypervisorCtx: hypervisorCtx,
	}

	var err error
	peer.runner, err = runner.StartRunner[L, R](
		hypervisorCtx,
		rescueCtx,
		hypervisorConfiguration,
		stateName,
		memoryName,
	)

	if err != nil {
		return nil, err
	}

	peer.VMPath = peer.runner.VMPath
	peer.VMPid = peer.runner.VMPid

	return peer, nil
}

func (peer *Peer[L, R, G]) MigrateFrom(
	ctx context.Context,
	devices []common.MigrateFromDevice,
	readers []io.Reader,
	writers []io.Writer,
	hooks mounter.MigrateFromHooks,
) (
	migratedPeer *MigratedPeer[L, R, G],
	errs error,
) {

	// TODO: Pass these in
	// TODO: This schema tweak function should be exposed / passed in
	var log types.Logger
	var met metrics.SiloMetrics
	tweakRemote := func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema {

		for _, d := range devices {
			if d.Name == schema.Name {
				newSchema, err := common.CreateIncomingSiloDevSchema(&d, schema)
				if err == nil {
					fmt.Printf("Tweaked schema %s\n", newSchema.EncodeAsBlock())
					return newSchema
				}
			}
		}

		// FIXME: Error. We didn't find the local device, or couldn't set it up.

		fmt.Printf("ERROR, didn't find local device defined %s\n", name)

		return schema
	}
	// TODO: Add the sync stuff here...
	tweakLocal := func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema {
		return schema
	}

	migratedPeer = &MigratedPeer[L, R, G]{
		devices: devices,
		runner:  peer.runner,
		Wait: func() error {
			fmt.Printf(" ### org migratedPeer.Wait\n")
			return nil
		},
	}

	// Migrate the devices from a protocol
	if len(readers) > 0 && len(writers) > 0 {
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		dg, err := common.MigrateFromPipe(log, met, migratedPeer.runner.VMPath, protocolCtx, readers, writers, tweakRemote, hooks.OnXferCustomData)
		if err != nil {
			return nil, err
		}

		migratedPeer.Wait = sync.OnceValue(func() error {
			fmt.Printf(" ### migratedPeer.Wait\n")
			defer cancelProtocolCtx()

			if dg != nil {
				err := dg.WaitForCompletion()
				if err != nil {
					return err
				}
			}
			return nil
		})

		// Save dg for future migrations, AND for things like reading config
		migratedPeer.setDG(dg)
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		dg, err := common.MigrateFromFS(log, met, migratedPeer.runner.VMPath, devices, tweakLocal)
		if err != nil {
			return nil, err
		}

		// Save dg for later usage, when we want to migrate from here etc
		migratedPeer.setDG(dg)

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	return
}
