package peer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type MigratedPeer[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	Wait  func() error
	Close func() error

	dg *devicegroup.DeviceGroup

	devices []MigrateFromDevice[L, R, G]
	runner  *runner.Runner[L, R, G]

	stage2Inputs []migrateFromStage
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
		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},

		stage2Inputs: migratedPeer.stage2Inputs,
	}

	configBasePath := ""
	for _, device := range migratedPeer.devices {
		if device.Name == packager.ConfigName {
			configBasePath = device.Base

			break
		}
	}

	if strings.TrimSpace(configBasePath) == "" {
		return nil, ErrConfigFileNotFound
	}

	packageConfigFile, err := os.Open(configBasePath)
	if err != nil {
		return nil, errors.Join(ErrCouldNotOpenConfigFile, err)
	}
	defer packageConfigFile.Close()

	var packageConfig snapshotter.PackageConfiguration
	if err := json.NewDecoder(packageConfigFile).Decode(&packageConfig); err != nil {
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
