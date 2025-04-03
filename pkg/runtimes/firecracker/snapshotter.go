package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	iutils "github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/logging/types"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
)

const (
	VSockName = "vsock.sock"

	DefaultBootArgs      = "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 clocksource=tsc nokaslr lapic=notscdeadline tsc=unstable"
	DefaultBootArgsNoPVM = "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 nokaslr"
)

var (
	ErrCouldNotOpenInputFile             = errors.New("could not open input file")
	ErrCouldNotCreateOutputFile          = errors.New("could not create output file")
	ErrCouldNotCopyFile                  = errors.New("error copying file")
	ErrCouldNotWritePadding              = errors.New("could not write padding")
	ErrCouldNotCopyDeviceFile            = errors.New("could not copy device file")
	ErrCouldNotStartVM                   = errors.New("could not start VM")
	ErrCouldNotMarshalPackageConfig      = errors.New("could not marshal package configuration")
	ErrCouldNotOpenPackageConfigFile     = errors.New("could not open package configuration file")
	ErrCouldNotWritePackageConfig        = errors.New("could not write package configuration")
	ErrCouldNotChownPackageConfigFile    = errors.New("could not change ownership of package configuration file")
	ErrCouldNotCreateChrootBaseDirectory = errors.New("could not create chroot base directory")
	ErrCouldNotCreateSnapshot            = errors.New("could not create snapshot")
)

func CreateSnapshot(log types.Logger, ctx context.Context, devices []SnapshotDevice, ioEngineSync bool,
	vmConfiguration VMConfiguration, livenessConfiguration LivenessConfiguration,
	hypervisorConfiguration HypervisorConfiguration, networkConfiguration NetworkConfiguration,
	agentConfiguration AgentConfiguration,
) (errs error) {
	if log != nil {
		log.Info().Msg("Creating firecracker VM snapshot")
		for _, s := range devices {
			log.Info().Str("input", s.Input).Str("output", s.Output).Str("name", s.Name).Msg("Snapshot device")
		}
	}

	if log != nil {
		log.Debug().Str("base-dir", hypervisorConfiguration.ChrootBaseDir).Msg("Creating ChrootBaseDir")
	}

	err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm)
	if err != nil {
		return errors.Join(ErrCouldNotCreateChrootBaseDirectory, err)
	}

	server, err := StartFirecrackerServer(
		ctx,
		log,
		hypervisorConfiguration.FirecrackerBin,
		hypervisorConfiguration.JailerBin,
		hypervisorConfiguration.ChrootBaseDir,
		hypervisorConfiguration.UID,
		hypervisorConfiguration.GID,
		hypervisorConfiguration.NetNS,
		hypervisorConfiguration.NumaNode,
		hypervisorConfiguration.CgroupVersion,
		hypervisorConfiguration.EnableOutput,
		hypervisorConfiguration.EnableInput,
	)

	if err != nil {
		return errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}
	defer server.Close()
	defer os.RemoveAll(filepath.Dir(server.VMPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	serverErr := make(chan error, 1)
	go func() {
		err := server.Wait()
		if err != nil {
			serverErr <- err
		}
	}()

	if log != nil {
		log.Debug().Str("vmpath", server.VMPath).Msg("Firecracker server running")
	}

	// Setup RPC bits required here.
	rpc := &FirecrackerRPC{
		Log:               log,
		VMPath:            server.VMPath,
		UID:               hypervisorConfiguration.UID,
		GID:               hypervisorConfiguration.GID,
		LivenessVSockPort: uint32(livenessConfiguration.LivenessVSockPort),
		AgentVSockPort:    uint32(agentConfiguration.AgentVSockPort),
	}

	err = rpc.Init()
	if err != nil {
		return err
	}
	defer rpc.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(server.VMPath, FirecrackerSocketName))
			},
		},
	}

	disks := []string{}
	for _, device := range devices {
		if strings.TrimSpace(device.Input) != "" {
			_, err := iutils.CopyFile(device.Input, filepath.Join(server.VMPath, device.Name), hypervisorConfiguration.UID, hypervisorConfiguration.GID)
			if err != nil {
				return errors.Join(ErrCouldNotCopyDeviceFile, err)
			}
		}
		if !slices.Contains(common.KnownNames, device.Name) || device.Name == common.DeviceDiskName {
			disks = append(disks, device.Name)
		}
	}

	ioEngine := SDKIOEngineAsync
	if ioEngineSync {
		ioEngine = SDKIOEngineSync
	}

	err = StartVMSDK(
		ctx,
		path.Join(server.VMPath, FirecrackerSocketName),
		common.DeviceKernelName,
		disks,
		ioEngine,
		vmConfiguration.CPUCount,
		vmConfiguration.MemorySize,
		vmConfiguration.CPUTemplate,
		vmConfiguration.BootArgs,
		networkConfiguration.Interface,
		networkConfiguration.MAC,
		VSockName,
		ipc.VSockCIDGuest,
	)

	if err != nil {
		return errors.Join(ErrCouldNotStartVM, err)
	}
	defer os.Remove(filepath.Join(server.VMPath, VSockName))

	if log != nil {
		log.Info().Msg("Started firecracker VM")
	}

	// Perform the RPC calls here...
	err = rpc.LivenessAndBeforeSuspendAndClose(ctx, livenessConfiguration.ResumeTimeout, agentConfiguration.ResumeTimeout)
	if err != nil {
		return err
	}

	err = createFinalSnapshot(ctx, client, agentConfiguration.AgentVSockPort,
		server.VMPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID)

	if err != nil {
		return err
	}

	err = copySnapshotFiles(devices, server.VMPath)
	if err != nil {
		return err
	}

	// Check for any firecracker server error here...
	select {
	case err := <-serverErr:
		return err
	default:
	}

	return
}

/**
 * Copy snapshot files
 *
 */
func copySnapshotFiles(devices []SnapshotDevice, vmPath string) error {

	for _, device := range devices {
		inputFile, err := os.Open(filepath.Join(vmPath, device.Name))
		if err != nil {
			return errors.Join(ErrCouldNotOpenInputFile, err)
		}
		defer inputFile.Close()

		err = os.MkdirAll(filepath.Dir(device.Output), os.ModePerm)
		if err != nil {
			return errors.Join(common.ErrCouldNotCreateOutputDir, err)
		}

		outputFile, err := os.OpenFile(device.Output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
		if err != nil {
			return errors.Join(ErrCouldNotCreateOutputFile, err)
		}
		defer outputFile.Close()

		deviceSize, err := io.Copy(outputFile, inputFile)
		if err != nil {
			return errors.Join(ErrCouldNotCopyFile, err)
		}

		if paddingLength := utils.GetBlockDevicePadding(deviceSize); paddingLength > 0 {
			_, err := outputFile.Write(make([]byte, paddingLength))
			if err != nil {
				return errors.Join(ErrCouldNotWritePadding, err)
			}
		}
	}
	return nil
}

/**
 * Create the final snapshot
 *
 */
func createFinalSnapshot(ctx context.Context, client *http.Client, vsockPort uint32, vmPath string, uid int, gid int) error {
	err := CreateSnapshotSDK(
		ctx,
		path.Join(vmPath, FirecrackerSocketName),
		common.DeviceStateName,
		common.DeviceMemoryName,
		SDKSnapshotTypeFull,
	)
	if err != nil {
		return errors.Join(ErrCouldNotCreateSnapshot, err)
	}

	packageConfig, err := json.Marshal(PackageConfiguration{
		AgentVSockPort: vsockPort,
	})
	if err != nil {
		return errors.Join(ErrCouldNotMarshalPackageConfig, err)
	}

	configFilename := filepath.Join(vmPath, common.DeviceConfigName)
	outputFile, err := os.OpenFile(configFilename, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return errors.Join(ErrCouldNotOpenPackageConfigFile, err)
	}
	defer outputFile.Close()

	_, err = outputFile.Write(packageConfig)
	if err != nil {
		return errors.Join(ErrCouldNotWritePackageConfig, err)
	}

	err = os.Chown(configFilename, uid, gid)
	if err != nil {
		return errors.Join(ErrCouldNotChownPackageConfigFile, err)
	}

	return nil
}
