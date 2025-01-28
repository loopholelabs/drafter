package peer

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

var ErrConfigFileNotFound = errors.New("config file not found")
var ErrCouldNotOpenConfigFile = errors.New("could not open config file")
var ErrCouldNotDecodeConfigFile = errors.New("could not decode config file")
var ErrCouldNotResumeRunner = errors.New("could not resume runner")

type Peer struct {
	log types.Logger

	// Devices
	dgLock     sync.Mutex
	dg         *devicegroup.DeviceGroup
	dgIncoming bool
	devices    []common.MigrateFromDevice
	cancelCtx  context.CancelFunc

	// Runtime
	runtimeProvider RuntimeProviderIfc
	VMPath          string
	VMPid           int

	// TODO: Going
	alreadyClosed bool
	alreadyWaited bool
}

// Callbacks for MigrateTO
type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllMigrationsCompleted func()
	OnProgress               func(p map[string]*migrator.MigrationProgress)
	GetXferCustomData        func() []byte
}

func (peer *Peer) Close() error {
	if peer.log != nil {
		peer.log.Debug().Msg("Peer.Close")
	}

	if peer.alreadyClosed {
		if peer.log != nil {
			peer.log.Debug().Msg("FIXME: Peer.Close called multiple times")
		}
		return nil
	}
	peer.alreadyClosed = true

	err := peer.runtimeProvider.Close()
	if err != nil {
		return err
	}

	if peer.log != nil {
		peer.log.Debug().Msg("Closing dg")
	}
	return peer.closeDG()
}

func (peer *Peer) Wait() error {
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

	defer func() {
		if peer.cancelCtx != nil {
			peer.cancelCtx()
		}
	}()

	peer.dgLock.Lock()
	if peer.dgIncoming && peer.dg != nil {
		peer.dgIncoming = false
		peer.dgLock.Unlock()
		if peer.log != nil {
			peer.log.Trace().Msg("waiting for device migrations to complete")
		}
		err := peer.dg.WaitForCompletion()
		if peer.log != nil {
			peer.log.Trace().Err(err).Msg("device migrations completed")
		}
		return err
	}
	peer.dgLock.Unlock()
	/*
		// TODO: Should this be here?
		if peer.runner != nil {
			return peer.runner.Wait()
		}

		// TODO: Should this be here?
		if peer.resumedRunner != nil {
			return peer.resumedRunner.Wait()
		}
	*/
	return nil
}

func (peer *Peer) setDG(dg *devicegroup.DeviceGroup, incoming bool) {
	peer.dgLock.Lock()
	peer.dg = dg
	peer.dgIncoming = incoming
	peer.dgLock.Unlock()
}

func (peer *Peer) closeDG() error {
	var err error
	peer.dgLock.Lock()
	if peer.dg != nil {
		err = peer.dg.CloseAll()
		peer.dg = nil
	}
	peer.dgLock.Unlock()
	return err
}

func StartPeer(ctx context.Context, rescueCtx context.Context,
	log types.Logger, rp RuntimeProviderIfc) (*Peer, error) {

	peer := &Peer{
		log:             log,
		runtimeProvider: rp,
	}

	err := peer.runtimeProvider.Start(ctx, rescueCtx)

	if err != nil {
		if log != nil {
			log.Warn().Err(err).Msg("error starting runner")
		}
		return nil, err
	}

	peer.VMPath = peer.runtimeProvider.DevicePath()
	peer.VMPid = peer.runtimeProvider.GetVMPid()

	if log != nil {
		log.Info().Str("vmpath", peer.VMPath).Int("vmpid", peer.VMPid).Msg("started peer runner")
	}
	return peer, nil
}

func (peer *Peer) MigrateFrom(ctx context.Context, devices []common.MigrateFromDevice,
	readers []io.Reader, writers []io.Writer, hooks mounter.MigrateFromHooks) error {

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

	peer.devices = devices

	// Migrate the devices from a protocol
	if len(readers) > 0 && len(writers) > 0 {
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		var slog types.Logger
		if peer.log != nil {
			slog = peer.log.SubLogger("silo")
		}
		dg, err := common.MigrateFromPipe(slog, met, peer.runtimeProvider.DevicePath(), protocolCtx, readers, writers, tweakRemote, hooks.OnXferCustomData)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from pipe")
			}
			return err
		}

		peer.cancelCtx = cancelProtocolCtx

		// Save dg for future migrations, AND for things like reading config
		peer.setDG(dg, true)

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

		dg, err := common.MigrateFromFS(slog, met, peer.runtimeProvider.DevicePath(), devices, tweakLocal)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from fs")
			}
			return err
		}

		// Save dg for later usage, when we want to migrate from here etc
		peer.setDG(dg, false)

		if peer.log != nil {
			peer.log.Info().Msg("migrated from fs successfully")
		}

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	return nil
}

func (peer *Peer) Resume(
	ctx context.Context,
	resumeTimeout,
	rescueTimeout time.Duration,
) error {

	if peer.log != nil {
		peer.log.Trace().Msg("resuming vm")
	}

	err := peer.runtimeProvider.Resume(resumeTimeout, rescueTimeout)

	if err != nil {
		if peer.log != nil {
			peer.log.Warn().Err(err).Msg("could not resume runner")
		}
		return errors.Join(ErrCouldNotResumeRunner, err)
	}

	if peer.log != nil {
		peer.log.Info().Msg("resumed vm")
	}

	return nil
}

/**
 * MigrateTo migrates to a remote VM.
 *
 *
 */
func (peer *Peer) MigrateTo(ctx context.Context, devices []common.MigrateToDevice,
	suspendTimeout time.Duration, concurrency int, readers []io.Reader, writers []io.Writer,
	hooks MigrateToHooks) error {

	if peer.log != nil {
		peer.log.Info().Msg("resumedPeer.MigrateTo")
	}

	// This manages the status of the VM - if it's suspended or not.
	vmState := common.NewVMStateMgr(ctx,
		peer.runtimeProvider.SuspendAndCloseAgentServer,
		suspendTimeout,
		peer.runtimeProvider.FlushData,
		hooks.OnBeforeSuspend,
		hooks.OnAfterSuspend,
	)

	err := common.MigrateToPipe(ctx, readers, writers, peer.dg, concurrency, hooks.OnProgress, vmState, devices, hooks.GetXferCustomData)
	if err != nil {
		if peer.log != nil {
			peer.log.Info().Err(err).Msg("error in resumedPeer.MigrateTo")
		}
		return err
	}

	if peer.log != nil {
		peer.log.Info().Msg("resumedPeer.MigrateTo completed successfuly")
	}

	return nil
}
