package peer

import (
	"context"
	"io"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
)

type Peer struct {
	log types.Logger

	VMPath string
	VMPid  int

	hypervisorCtx context.Context

	runner *runner.Runner[struct{}, ipc.AgentServerRemote[struct{}], struct{}]

	alreadyClosed bool
	alreadyWaited bool
}

func (peer Peer) Close() error {
	if peer.log != nil {
		peer.log.Debug().Msg("Peer.Wait")
	}

	if peer.alreadyClosed {
		if peer.log != nil {
			peer.log.Debug().Msg("FIXME: Peer.Close called multiple times")
		}
		return nil
	}
	peer.alreadyClosed = true

	if peer.runner != nil {
		err := peer.runner.Close()
		if err != nil {
			return err
		}
		return peer.runner.Wait()
	}
	return nil
}

func (peer Peer) Wait() error {
	if peer.log != nil {
		peer.log.Debug().Msg("Peer.Wait")
	}

	if peer.alreadyWaited {
		if peer.log != nil {
			peer.log.Debug().Msg("FIXME: Peer.Wait called multiple times")
		}
		return nil
	}
	peer.alreadyWaited = true

	if peer.runner != nil {
		return peer.runner.Wait()
	}
	return nil
}

func StartPeer(hypervisorCtx context.Context, rescueCtx context.Context,
	hypervisorConfiguration snapshotter.HypervisorConfiguration,
	stateName string, memoryName string, log types.Logger) (*Peer, error) {
	peer := &Peer{
		hypervisorCtx: hypervisorCtx,
		log:           log,
	}

	var err error
	peer.runner, err = runner.StartRunner[struct{}, ipc.AgentServerRemote[struct{}]](
		hypervisorCtx,
		rescueCtx,
		hypervisorConfiguration,
		stateName,
		memoryName,
	)

	if err != nil {
		if log != nil {
			log.Warn().Err(err).Msg("error starting runner")
		}
		return nil, err
	}

	peer.VMPath = peer.runner.VMPath
	peer.VMPid = peer.runner.VMPid

	if log != nil {
		log.Info().Str("vmpath", peer.VMPath).Int("vmpid", peer.VMPid).Msg("started peer runner")
	}
	return peer, nil
}

func (peer *Peer) MigrateFrom(ctx context.Context, devices []common.MigrateFromDevice,
	readers []io.Reader, writers []io.Writer, hooks mounter.MigrateFromHooks) (*MigratedPeer, error) {

	if peer.log != nil {
		peer.log.Info().Msg("started MigrateFrom")
	}

	// TODO: Pass these in
	// TODO: This schema tweak function should be exposed / passed in
	var met metrics.SiloMetrics
	tweakRemote := func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema {

		for _, d := range devices {
			if d.Name == schema.Name {
				newSchema, err := common.CreateIncomingSiloDevSchema(&d, schema)
				if err == nil {
					if peer.log != nil {
						peer.log.Debug().Str("schema", string(newSchema.EncodeAsBlock())).Msg("incoming schema")
					}
					return newSchema
				}
			}
		}

		// FIXME: Error. We didn't find the local device, or couldn't set it up.
		if peer.log != nil {
			peer.log.Error().Str("name", name).Msg("unknown device name")
			// We should probably relay an error here...
		}

		return schema
	}
	// TODO: Add the sync stuff here...
	tweakLocal := func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema {
		return schema
	}

	migratedPeer := &MigratedPeer{
		devices: devices,
		runner:  peer.runner,
		log:     peer.log,
	}

	// Migrate the devices from a protocol
	if len(readers) > 0 && len(writers) > 0 {
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		var slog types.Logger
		if peer.log != nil {
			slog = peer.log.SubLogger("silo")
		}
		dg, err := common.MigrateFromPipe(slog, met, migratedPeer.runner.VMPath, protocolCtx, readers, writers, tweakRemote, hooks.OnXferCustomData)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from pipe")
			}
			return nil, err
		}

		migratedPeer.cancelCtx = cancelProtocolCtx

		// Save dg for future migrations, AND for things like reading config
		migratedPeer.setDG(dg, true)

		if peer.log != nil {
			peer.log.Info().Msg("migrated from pipe successfully")
		}
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		var slog types.Logger
		if peer.log != nil {
			slog = peer.log.SubLogger("silo")
		}

		dg, err := common.MigrateFromFS(slog, met, migratedPeer.runner.VMPath, devices, tweakLocal)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from fs")
			}
			return nil, err
		}

		// Save dg for later usage, when we want to migrate from here etc
		migratedPeer.setDG(dg, false)

		if peer.log != nil {
			peer.log.Info().Msg("migrated from fs successfully")
		}

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	return migratedPeer, nil
}
