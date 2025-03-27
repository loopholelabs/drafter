package firecracker

import (
	"context"
	"errors"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
)

const SDKSnapshotTypeFull = "Full"
const SDKSnapshotTypeMsync = "Msync"
const SDKSnapshotTypeMsyncAndState = "MsyncAndState"

var statePaused = "Paused"
var stateRunning = "Running"

var backendTypeFile = "File"

func ResumeSnapshotSDK(ctx context.Context, socketPath string, statePath string, memoryPath string) error {

	logger := logrus.New()
	log := logger.WithField("subsystem", "firecracker")

	log.WithField("socketPath", socketPath).Info("Loading snapshot")

	client := sdk.NewClient(socketPath, log, true)

	_, err := client.LoadSnapshot(ctx, &models.SnapshotLoadParams{
		EnableDiffSnapshots: false,
		MemBackend: &models.MemoryBackend{
			BackendType: &backendTypeFile,
			BackendPath: &memoryPath,
		},
		ResumeVM:     true,
		SnapshotPath: &statePath,
		Shared:       true,
	})

	if err != nil {
		return errors.Join(ErrCouldNotResumeSnapshot, err)
	}

	return nil
}

/**
 * Pause the VM and create a snapshot.
 *
 */
func CreateSnapshotSDK(ctx context.Context, socketPath string, statePath string, memoryPath string, snapshotType string) error {
	logger := logrus.New()
	log := logger.WithField("subsystem", "firecracker")

	log.WithField("socketPath", socketPath).Info("Creating snapshot")

	client := sdk.NewClient(socketPath, log, true)

	if snapshotType != SDKSnapshotTypeMsync {
		_, err := client.PatchVM(ctx, &models.VM{
			State: &statePaused,
		})
		if err != nil {
			return err
		}
	}

	_, err := client.CreateSnapshot(ctx, &models.SnapshotCreateParams{
		MemFilePath:  &memoryPath,
		SnapshotPath: &statePath,
		SnapshotType: snapshotType,
	})

	return err
}
