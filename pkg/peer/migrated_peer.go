package peer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type MigratedPeer struct {
	cancelCtx context.CancelFunc

	log types.Logger

	dgLock     sync.Mutex
	dg         *devicegroup.DeviceGroup
	dgIncoming bool

	devices []common.MigrateFromDevice
	runner  *runner.Runner[struct{}, ipc.AgentServerRemote[struct{}], struct{}]

	alreadyClosed bool
	alreadyWaited bool
}

func (migratedPeer *MigratedPeer) Close() error {
	if migratedPeer.log != nil {
		migratedPeer.log.Debug().Msg("migratedPeer.Close")
	}
	if migratedPeer.alreadyClosed {
		if migratedPeer.log != nil {
			migratedPeer.log.Trace().Msg("FIXME: MigratedPeer.Close called multiple times")
		}
		return nil
	}
	migratedPeer.alreadyClosed = true

	// We have to close the runner before we close the devices
	err := migratedPeer.runner.Close()
	if err != nil {
		return err
	}

	// Close any Silo devices
	return migratedPeer.closeDG()
}

func (migratedPeer *MigratedPeer) Wait() error {
	if migratedPeer.log != nil {
		migratedPeer.log.Debug().Msg("migratedPeer.Wait")
	}
	if migratedPeer.alreadyWaited {
		if migratedPeer.log != nil {
			migratedPeer.log.Trace().Msg("FIXME: MigratedPeer.Wait called multiple times")
		}
		return nil
	}
	migratedPeer.alreadyWaited = true

	defer func() {
		if migratedPeer.cancelCtx != nil {
			migratedPeer.cancelCtx()
		}
	}()

	migratedPeer.dgLock.Lock()
	if migratedPeer.dgIncoming && migratedPeer.dg != nil {
		migratedPeer.dgIncoming = false
		migratedPeer.dgLock.Unlock()
		if migratedPeer.log != nil {
			migratedPeer.log.Trace().Msg("waiting for device migrations to complete")
		}
		err := migratedPeer.dg.WaitForCompletion()
		if migratedPeer.log != nil {
			migratedPeer.log.Trace().Err(err).Msg("device migrations completed")
		}
		return err
	}
	migratedPeer.dgLock.Unlock()

	return nil
}

func (migratedPeer *MigratedPeer) setDG(dg *devicegroup.DeviceGroup, incoming bool) {
	migratedPeer.dgLock.Lock()
	migratedPeer.dg = dg
	migratedPeer.dgIncoming = incoming
	migratedPeer.dgLock.Unlock()
}

func (migratedPeer *MigratedPeer) closeDG() error {
	var err error
	migratedPeer.dgLock.Lock()
	if migratedPeer.dg != nil {
		err = migratedPeer.dg.CloseAll()
		migratedPeer.dg = nil
	}
	migratedPeer.dgLock.Unlock()
	return err
}

func (migratedPeer *MigratedPeer) Resume(
	ctx context.Context,

	resumeTimeout,
	rescueTimeout time.Duration,

	agentServerLocal struct{},
	agentServerHooks ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}],

	snapshotLoadConfiguration runner.SnapshotLoadConfiguration,
) (*ResumedPeer, error) {
	resumedPeer := &ResumedPeer{
		dg:  migratedPeer.dg,
		log: migratedPeer.log,
	}

	if migratedPeer.log != nil {
		migratedPeer.log.Trace().Msg("resuming vm")
	}

	// Read from the config device
	configFileData, err := os.ReadFile(path.Join(migratedPeer.runner.VMPath, packager.ConfigName))
	if err != nil {
		return nil, errors.Join(ErrCouldNotOpenConfigFile, err)
	}

	// Find the first 0 byte...
	firstZero := 0
	for i := 0; i < len(configFileData); i++ {
		if configFileData[i] == 0 {
			firstZero = i
			break
		}
	}
	configFileData = configFileData[:firstZero]

	if migratedPeer.log != nil {
		migratedPeer.log.Trace().Str("config", string(configFileData)).Msg("resuming config")
	}

	var packageConfig snapshotter.PackageConfiguration
	if err := json.Unmarshal(configFileData, &packageConfig); err != nil {
		return nil, errors.Join(ErrCouldNotDecodeConfigFile, err)
	}

	resumedPeer.resumedRunner, err = migratedPeer.runner.Resume(
		ctx,

		resumeTimeout,
		rescueTimeout,
		packageConfig.AgentVSockPort,

		agentServerLocal,
		agentServerHooks,

		snapshotLoadConfiguration,
	)
	if err != nil {
		if migratedPeer.log != nil {
			migratedPeer.log.Warn().Err(err).Msg("could not resume runner")
		}
		return nil, errors.Join(ErrCouldNotResumeRunner, err)
	}
	resumedPeer.Remote = resumedPeer.resumedRunner.Remote

	if migratedPeer.log != nil {
		migratedPeer.log.Info().Msg("resumed vm")
	}

	return resumedPeer, nil
}
