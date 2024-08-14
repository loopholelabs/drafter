package peer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
)

type MigratedPeer struct {
	Wait  func() error
	Close func() error

	devices []MigrateFromDevice
	runner  *runner.Runner

	stage2Inputs []stage2
}

func (migratedPeer *MigratedPeer) Resume(
	ctx context.Context,

	resumeTimeout,
	rescueTimeout time.Duration,

	snapshotLoadConfiguration runner.SnapshotLoadConfiguration,
) (resumedPeer *ResumedPeer, errs error) {
	resumedPeer = &ResumedPeer{
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

		snapshotLoadConfiguration,
	)
	if err != nil {
		return nil, errors.Join(ErrCouldNotResumeRunner, err)
	}

	resumedPeer.Wait = resumedPeer.resumedRunner.Wait
	resumedPeer.Close = resumedPeer.resumedRunner.Close

	return resumedPeer, nil
}
