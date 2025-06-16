package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

type Peer struct {
	log  types.Logger
	met  metrics.SiloMetrics
	dmet *common.DrafterMetrics

	// Devices
	dgLock  sync.Mutex
	dg      *devicegroup.DeviceGroup
	devices []common.MigrateFromDevice

	// Runtime
	runtimeProvider runtimes.RuntimeProviderIfc
	VMPath          string
	VMPid           int

	// Errors
	backgroundErr chan error

	instanceID string

	metricFlushDataOps           int64
	metricFlushDataTimeMs        int64
	metricVMRunning              int64
	metricMigratingTo            int64
	metricMigratingFrom          int64
	metricMigratingFromWaitReady int64
}

type PeerMetrics struct {
	FlushDataOps           int64
	FlushDataTimeMs        int64
	VMRunning              int64
	MigratingTo            int64
	MigratingFrom          int64
	MigratingFromWaitReady int64
}

// Callbacks for MigrateTO
type MigrateToHooks struct {
	OnBeforeSuspend          func()
	OnAfterSuspend           func()
	OnAllMigrationsCompleted func()
	OnProgress               func(p map[string]*migrator.MigrationProgress)
	GetXferCustomData        func() []byte
}

// Callbacks for MigrateFrom
type MigrateFromHooks struct {
	OnLocalDeviceRequested func(localDeviceID uint32, name string)
	OnLocalDeviceExposed   func(localDeviceID uint32, path string)
	OnCompletion           func()

	OnLocalAllDevicesRequested func()

	OnXferCustomData func([]byte)
}

func (peer *Peer) GetMetrics() *PeerMetrics {
	return &PeerMetrics{
		FlushDataOps:           atomic.LoadInt64(&peer.metricFlushDataOps),
		FlushDataTimeMs:        atomic.LoadInt64(&peer.metricFlushDataTimeMs),
		VMRunning:              atomic.LoadInt64(&peer.metricVMRunning),
		MigratingTo:            atomic.LoadInt64(&peer.metricMigratingTo),
		MigratingFrom:          atomic.LoadInt64(&peer.metricMigratingFrom),
		MigratingFromWaitReady: atomic.LoadInt64(&peer.metricMigratingFromWaitReady),
	}
}

func (peer *Peer) CloseRuntime() error {
	if peer.log != nil {
		peer.log.Info().Str("id", peer.instanceID).Msg("Peer.Close")
	}

	// Try to close the runtimeProvider first
	errRuntime := peer.runtimeProvider.Close(peer.GetDG())
	if errRuntime != nil {
		peer.log.Error().Err(errRuntime).Msg("Error closing runtime")
	}
	return errRuntime
}

func (peer *Peer) Close() error {
	if peer.log != nil {
		peer.log.Info().Str("id", peer.instanceID).Msg("Peer.Close")
	}

	// Try to close the runtimeProvider first
	errRuntime := peer.runtimeProvider.Close(peer.GetDG())
	if errRuntime != nil {
		peer.log.Error().Err(errRuntime).Msg("Error closing runtime")
	}

	if peer.log != nil {
		peer.log.Debug().Msg("Closing dg")
	}
	errDG := peer.closeDG()
	// Return

	// Remove this from metrics
	if peer.met != nil {
		peer.met.RemoveAllID(peer.instanceID)
	}

	return errors.Join(errRuntime, errDG)
}

func (peer *Peer) BackgroundErr() chan error {
	return peer.backgroundErr
}

func (peer *Peer) setDG(dg *devicegroup.DeviceGroup) {
	peer.dgLock.Lock()
	peer.dg = dg
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

func (peer *Peer) GetDG() *devicegroup.DeviceGroup {
	peer.dgLock.Lock()
	defer peer.dgLock.Unlock()
	return peer.dg
}

func StartPeer(ctx context.Context, rescueCtx context.Context,
	log types.Logger, met metrics.SiloMetrics, dmet *common.DrafterMetrics, id string, rp runtimes.RuntimeProviderIfc) (*Peer, error) {

	peer := &Peer{
		log:             log,
		met:             met,
		dmet:            dmet,
		instanceID:      fmt.Sprintf("%s-%s", uuid.NewString(), id),
		runtimeProvider: rp,
		backgroundErr:   make(chan error, 12),
	}

	err := peer.runtimeProvider.Start(ctx, rescueCtx, peer.backgroundErr)

	if err != nil {
		if log != nil {
			log.Warn().Err(err).Msg("error starting runtime")
		}
		return nil, err
	}

	peer.VMPath = peer.runtimeProvider.DevicePath()
	peer.VMPid = peer.runtimeProvider.GetVMPid()

	if log != nil {
		log.Info().Str("vmpath", peer.VMPath).Int("vmpid", peer.VMPid).Msg("started peer runtime")
	}
	return peer, nil
}

func (peer *Peer) SetInstanceID(id string) {
	peer.instanceID = id
}

func (peer *Peer) MigrateFrom(ctx context.Context, devices []common.MigrateFromDevice,
	readers []io.Reader, writers []io.Writer, hooks MigrateFromHooks) error {

	atomic.StoreInt64(&peer.metricMigratingFrom, 1)

	// FIXME: Going
	if peer.dmet != nil {
		// Set migratingTo to 1 while we are migrating away.
		peer.dmet.MetricMigratingFrom.WithLabelValues(peer.instanceID).Set(1)
	}

	if peer.log != nil {
		peer.log.Info().Msg("started MigrateFrom")
	}

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
		dg, err := common.MigrateFromPipe(slog, peer.met, peer.instanceID, peer.runtimeProvider.DevicePath(), protocolCtx, readers, writers, tweakRemote, hooks.OnXferCustomData)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from pipe")
			}
			cancelProtocolCtx()
			return err
		}

		// Save dg for future migrations, AND for things like reading config
		peer.setDG(dg)

		if peer.log != nil {
			peer.log.Info().Msg("migrated from pipe successfully")
		}

		// Monitor the transfer, and report any error if it happens
		go func() {
			defer cancelProtocolCtx()
			defer atomic.StoreInt64(&peer.metricMigratingFrom, 0)

			// FIXME: Going
			if peer.dmet != nil {
				defer peer.dmet.MetricMigratingFrom.WithLabelValues(peer.instanceID).Set(0)
			}

			if peer.log != nil {
				peer.log.Trace().Msg("waiting for device migrations to complete")
			}
			err := dg.WaitForCompletion()
			if peer.log != nil {
				if err != nil {
					peer.log.Error().Err(err).Msg("device migrations completed")
				} else {
					peer.log.Info().Msg("device migrations completed")
				}
			}
			if err == nil {
				atomic.StoreInt64(&peer.metricMigratingFromWaitReady, 1)

				// FIXME: Going
				if peer.dmet != nil {
					peer.dmet.MetricMigratingFromWaitReady.WithLabelValues(peer.instanceID).Set(1)
				}
				err = dg.WaitForReady()
				atomic.StoreInt64(&peer.metricMigratingFromWaitReady, 0)

				// FIXME: Going
				if peer.dmet != nil {
					peer.dmet.MetricMigratingFromWaitReady.WithLabelValues(peer.instanceID).Set(0)
				}
				if peer.log != nil {
					if err != nil {
						peer.log.Error().Err(err).Msg("device migrations completed and ready")
					} else {
						peer.log.Info().Msg("device migrations completed and ready")
					}
				}

				if err == nil {
					// peer.showDeviceHashes("MigrateFrom")

					if hooks.OnCompletion != nil {
						hooks.OnCompletion()
					}
				} else {
					select {
					case peer.backgroundErr <- err:
					default:
					}
				}
			} else {
				select {
				case peer.backgroundErr <- err:
				default:
				}
			}
		}()
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		defer atomic.StoreInt64(&peer.metricMigratingFrom, 0)

		// FIXME: Going
		if peer.dmet != nil {
			defer peer.dmet.MetricMigratingFrom.WithLabelValues(peer.instanceID).Set(0)
		}

		var slog types.Logger
		if peer.log != nil {
			slog = peer.log.SubLogger("silo")
		}

		dg, err := common.MigrateFromFS(slog, peer.met, peer.instanceID, peer.runtimeProvider.DevicePath(), devices, tweakLocal)
		if err != nil {
			if peer.log != nil {
				peer.log.Warn().Err(err).Msg("error migrating from fs")
			}
			return err
		}

		// Save dg for later usage, when we want to migrate from here etc
		peer.setDG(dg)

		if peer.log != nil {
			peer.log.Info().Msg("migrated from fs successfully")
		}

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}

		if hooks.OnCompletion != nil {
			go hooks.OnCompletion()
		}
	}

	return nil
}

func (peer *Peer) Resume(ctx context.Context, resumeTimeout time.Duration, rescueTimeout time.Duration) error {

	if peer.log != nil {
		peer.log.Trace().Msg("resuming runtime")
	}

	resumeCtx, resumeCancel := context.WithTimeout(ctx, resumeTimeout)
	err := peer.runtimeProvider.Resume(resumeCtx, rescueTimeout, peer.GetDG(), peer.backgroundErr)
	resumeCancel()

	if err != nil {
		if peer.log != nil {
			peer.log.Warn().Err(err).Msg("could not resume runtime")
		}
		return err
	}

	atomic.StoreInt64(&peer.metricVMRunning, 1)

	// FIXME: Going
	if peer.dmet != nil {
		peer.dmet.MetricVMRunning.WithLabelValues(peer.instanceID).Set(1)
	}

	if peer.log != nil {
		peer.log.Info().Msg("resumed runtime")
	}

	return nil
}

/**
 * MigrateTo migrates to a remote VM.
 *
 *
 */
func (peer *Peer) MigrateTo(ctx context.Context, devices []common.MigrateToDevice,
	suspendTimeout time.Duration, options *common.MigrateToOptions, readers []io.Reader, writers []io.Writer,
	hooks MigrateToHooks) error {

	atomic.StoreInt64(&peer.metricMigratingTo, 1)
	defer atomic.StoreInt64(&peer.metricMigratingTo, 0)

	// FIXME: Going
	if peer.dmet != nil {
		// Set migratingTo to 1 while we are migrating away.
		peer.dmet.MetricMigratingTo.WithLabelValues(peer.instanceID).Set(1)
		defer peer.dmet.MetricMigratingTo.WithLabelValues(peer.instanceID).Set(0)
	}

	if peer.log != nil {
		peer.log.Info().Msg("peer.MigrateTo")
	}

	suspend := func(ctx context.Context, timeout time.Duration) error {
		err := peer.runtimeProvider.Suspend(ctx, timeout, peer.GetDG())
		if err == nil {
			atomic.StoreInt64(&peer.metricVMRunning, 0)

			// FIXME: Going
			if peer.dmet != nil {
				peer.dmet.MetricVMRunning.WithLabelValues(peer.instanceID).Set(0)
			}
		}
		return err
	}

	flushData := func(ctx context.Context) error {
		ctime := time.Now()
		err := peer.runtimeProvider.FlushData(ctx, peer.GetDG())
		ms := time.Since(ctime).Milliseconds()
		if err == nil {
			atomic.AddInt64(&peer.metricFlushDataOps, 1)
			atomic.AddInt64(&peer.metricFlushDataTimeMs, ms)

			// FIXME: Going
			if peer.dmet != nil {
				peer.dmet.MetricFlushDataTimeMS.WithLabelValues(peer.instanceID).Add(float64(ms))
				peer.dmet.MetricFlushDataOps.WithLabelValues(peer.instanceID).Inc()
			}
		}
		return err
	}

	// This manages the status of the VM - if it's suspended or not.
	vmState := common.NewVMStateMgr(ctx,
		suspend,
		suspendTimeout,
		flushData,
		hooks.OnBeforeSuspend,
		hooks.OnAfterSuspend,
	)

	err := common.MigrateToPipe(ctx, readers, writers, peer.dg, options, hooks.OnProgress, vmState, devices, hooks.GetXferCustomData, peer.met, peer.instanceID)
	if err != nil {
		if peer.log != nil {
			peer.log.Info().Err(err).Msg("error in peer.MigrateTo")
		}
		return err
	}

	if peer.log != nil {
		peer.log.Info().Msg("peer.MigrateTo completed successfuly")
	}

	// peer.showDeviceHashes("MigrateTo")

	return nil
}
