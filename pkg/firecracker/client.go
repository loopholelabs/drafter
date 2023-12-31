package firecracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	v1 "github.com/loopholelabs/drafter/pkg/api/http/firecracker/v1"
)

var (
	ErrCouldNotSetBootSource        = errors.New("could not set boot source")
	ErrCouldNotSetDrive             = errors.New("could not set drive")
	ErrCouldNotSetMachineConfig     = errors.New("could not set machine config")
	ErrCouldNotSetVSock             = errors.New("could not set vsock")
	ErrCouldNotSetNetworkInterfaces = errors.New("could not set network interfaces")
	ErrCouldNotStartInstance        = errors.New("could not start instance")
	ErrCouldNotStopInstance         = errors.New("could not stop instance")
	ErrCouldNotPauseInstance        = errors.New("could not pause instance")
	ErrCouldNotCreateSnapshot       = errors.New("could not create snapshot")
	ErrCouldNotResumeSnapshot       = errors.New("could not resume snapshot")
	ErrCouldNotFlushSnapshot        = errors.New("could not flush snapshot")
)

func submitJSON(method string, client *http.Client, body any, resource string) error {
	p, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, "http://localhost/"+resource, bytes.NewReader(p))
	if err != nil {
		return err
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode >= 300 {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}

		return errors.New(string(b))
	}

	return nil
}

func StartVM(
	client *http.Client,

	initramfsPath string,
	kernelPath string,
	diskPath string,

	cpuCount int,
	memorySize int,
	bootArgs string,

	hostInterface string,
	hostMAC string,

	vsockPath string,
	vsockCID int,
) error {
	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.BootSource{
			InitrdPath:      initramfsPath,
			KernelImagePath: kernelPath,
			BootArgs:        bootArgs,
		},
		"boot-source",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetBootSource, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.Drive{
			DriveID:      "root",
			PathOnHost:   diskPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		},
		path.Join("drives", "root"),
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetDrive, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.MachineConfig{
			VCPUCount:  cpuCount,
			MemSizeMib: memorySize,
		},
		"machine-config",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetMachineConfig, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.VSock{
			GuestCID: vsockCID,
			UDSPath:  vsockPath,
		},
		"vsock",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetVSock, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.NetworkInterface{
			IfaceID:     hostInterface,
			GuestMAC:    hostMAC,
			HostDevName: hostInterface,
		},
		path.Join("network-interfaces", hostInterface),
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetNetworkInterfaces, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.Action{
			ActionType: "InstanceStart",
		},
		"actions",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotStartInstance, err)
	}

	return nil
}

func StopVM(
	client *http.Client,
) error {
	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.Action{
			ActionType: "SendCtrlAltDel",
		},
		"actions",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotStopInstance, err)
	}

	return nil
}

func CreateSnapshot(
	client *http.Client,

	statePath,
	memoryPath string,
) error {
	if err := submitJSON(
		http.MethodPatch,
		client,
		&v1.VirtualMachineStateRequest{
			State: "Paused",
		},
		"vm",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotPauseInstance, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.SnapshotCreateRequest{
			SnapshotType:   "Full",
			SnapshotPath:   statePath,
			MemoryFilePath: memoryPath,
		},
		"snapshot/create",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotCreateSnapshot, err)
	}

	return nil
}

func ResumeSnapshot(
	client *http.Client,

	statePath,
	memoryPath string,
) error {
	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.SnapshotLoadRequest{
			SnapshotPath: statePath,
			MemoryBackend: v1.SnapshotLoadRequestMemoryBackend{
				BackendType: "File",
				BackendPath: memoryPath,
			},
			EnableDiffSnapshots:  false,
			ResumeVirtualMachine: true,
		},
		"snapshot/load",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotResumeSnapshot, err)
	}

	return nil
}

func FlushSnapshot(
	client *http.Client,

	statePath string,
) error {
	if err := submitJSON(
		http.MethodPatch,
		client,
		&v1.VirtualMachineStateRequest{
			State: "Paused",
		},
		"vm",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotPauseInstance, err)
	}

	if err := submitJSON(
		http.MethodPut,
		client,
		&v1.SnapshotNoMemoryCreateRequest{
			SnapshotPath: statePath,
		},
		"snapshot-nomemory/create",
	); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotFlushSnapshot, err)
	}

	return nil
}
