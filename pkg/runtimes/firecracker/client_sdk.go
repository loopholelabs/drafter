package firecracker

import (
	"context"
	"errors"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
)

var (
	ErrCouldNotSetBootSource        = errors.New("could not set boot source")
	ErrCouldNotSetDrive             = errors.New("could not set drive")
	ErrCouldNotSetMachineConfig     = errors.New("could not set machine config")
	ErrCouldNotSetVSock             = errors.New("could not set vsock")
	ErrCouldNotSetNetworkInterfaces = errors.New("could not set network interfaces")
	ErrCouldNotStartInstance        = errors.New("could not start instance")
)

const SDKSnapshotTypeFull = "Full"
const SDKSnapshotTypeMsync = "Msync"
const SDKSnapshotTypeMsyncAndState = "MsyncAndState"
const SDKActionInstanceStart = "InstanceStart"
const SDKIOEngineSync = "Sync"
const SDKIOEngineAsync = "Async"
const SDKVMStatePaused = "Paused"
const SDKVMStateRunning = "Running"
const SDKBackendTypeFile = "File"

/**
 * Resume from a snapshot.
 *
 */
func ResumeSnapshotSDK(ctx context.Context, socketPath string, statePath string, memoryPath string) error {

	logger := logrus.New()
	log := logger.WithField("subsystem", "firecracker")

	log.WithField("socketPath", socketPath).Info("Loading snapshot")

	client := sdk.NewClient(socketPath, log, true)

	_, err := client.LoadSnapshot(ctx, &models.SnapshotLoadParams{
		EnableDiffSnapshots: false,
		MemBackend: &models.MemoryBackend{
			BackendType: sdk.String(SDKBackendTypeFile),
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
			State: sdk.String(SDKVMStatePaused),
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

/**
 * Start VM
 *
 */
func StartVMSDK(ctx context.Context, socketPath string, kernelPath string,
	disks []string, ioEngine string, cpuCount int64, memorySize int64,
	cpuTemplate string, bootArgs string, hostInterface string,
	hostMAC string, vsockPath string, vsockCID int64) error {

	logger := logrus.New()
	log := logger.WithField("subsystem", "firecracker")

	log.WithField("socketPath", socketPath).Info("Creating snapshot")

	client := sdk.NewClient(socketPath, log, true)

	_, err := client.PutGuestBootSource(ctx, &models.BootSource{
		KernelImagePath: &kernelPath,
		BootArgs:        bootArgs,
	})
	if err != nil {
		return errors.Join(ErrCouldNotSetBootSource, err)
	}

	for _, disk := range disks {
		_, err := client.PutGuestDriveByID(ctx, disk, &models.Drive{
			DriveID:      &disk,
			IoEngine:     &ioEngine,
			PathOnHost:   &disk,
			IsRootDevice: sdk.Bool(false),
			IsReadOnly:   sdk.Bool(false),
		})
		if err != nil {
			return errors.Join(ErrCouldNotSetDrive, err)
		}
	}

	_, err = client.PutMachineConfiguration(ctx, &models.MachineConfiguration{
		VcpuCount:   &cpuCount,
		MemSizeMib:  &memorySize,
		CPUTemplate: models.CPUTemplate(cpuTemplate).Pointer(),
	})
	if err != nil {
		return errors.Join(ErrCouldNotSetMachineConfig, err)
	}

	_, err = client.PutGuestVsock(ctx, &models.Vsock{
		GuestCid: &vsockCID,
		UdsPath:  &vsockPath,
	})

	if err != nil {
		return errors.Join(ErrCouldNotSetVSock, err)
	}

	_, err = client.PutGuestNetworkInterfaceByID(ctx, hostInterface, &models.NetworkInterface{
		IfaceID:     &hostInterface,
		GuestMac:    hostMAC,
		HostDevName: &hostInterface,
	})

	if err != nil {
		return errors.Join(ErrCouldNotSetNetworkInterfaces, err)
	}

	_, err = client.CreateSyncAction(ctx, &models.InstanceActionInfo{
		ActionType: sdk.String(SDKActionInstanceStart),
	})

	if err != nil {
		return errors.Join(ErrCouldNotStartInstance, err)
	}

	return nil
}
