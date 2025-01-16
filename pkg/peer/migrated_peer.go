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
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type MigratedPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Wait  func() error
	Close func() error

	DgLock sync.Mutex
	Dg     *devicegroup.DeviceGroup

	devices []common.MigrateFromDevice
	runner  *runner.Runner[L, R, G]
}

func (migratedPeer *MigratedPeer[L, R, G]) Resume(
	ctx context.Context,

	resumeTimeout,
	rescueTimeout time.Duration,

	agentServerLocal L,
	agentServerHooks ipc.AgentServerAcceptHooks[R, G],

	snapshotLoadConfiguration runner.SnapshotLoadConfiguration,
) (resumedPeer *ResumedPeer[L, R, G], errs error) {
	resumedPeer = &ResumedPeer[L, R, G]{
		Dg: migratedPeer.Dg,
		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
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
		return nil, errors.Join(ErrCouldNotResumeRunner, err)
	}
	resumedPeer.Remote = resumedPeer.resumedRunner.Remote

	resumedPeer.Wait = resumedPeer.resumedRunner.Wait
	resumedPeer.Close = resumedPeer.resumedRunner.Close

	return resumedPeer, nil
}
