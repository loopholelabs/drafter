package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/loopholelabs/logging/types"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
)

type SnapshotDevice struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

type LivenessConfiguration struct {
	LivenessVSockPort uint32
	ResumeTimeout     time.Duration
}

type AgentConfiguration struct {
	AgentVSockPort uint32
	ResumeTimeout  time.Duration
}

type PackageConfiguration struct {
	AgentVSockPort uint32 `json:"agentVSockPort"`
}

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
	ErrCouldNotOpenSourceFile            = errors.New("could not open source file")
	ErrCouldNotCreateDestinationFile     = errors.New("could not create destination file")
	ErrCouldNotCopyFileContent           = errors.New("could not copy file content")
	ErrCouldNotChangeFileOwner           = errors.New("could not change file owner")

	// TODO Cleanup
	ErrCouldNotStartAgentServer              = errors.New("could not start agent server")
	ErrCouldNotCloseAcceptingAgent           = errors.New("could not close accepting agent")
	ErrCouldNotOpenLivenessServer            = errors.New("could not open liveness server")
	ErrCouldNotChownLivenessServerVSock      = errors.New("could not change ownership of liveness server VSock")
	ErrCouldNotChownAgentServerVSock         = errors.New("could not change ownership of agent server VSock")
	ErrCouldNotReceiveAndCloseLivenessServer = errors.New("could not receive and close liveness server")
	ErrCouldNotAcceptAgentConnection         = errors.New("could not accept agent connection")
	ErrCouldNotBeforeSuspend                 = errors.New("error before suspend")

	// TODO Dedup
	ErrCouldNotChownVSockPath       = errors.New("could not change ownership of vsock path")
	ErrCouldNotAcceptAgent          = errors.New("could not accept agent")
	ErrCouldNotCallAfterResumeRPC   = errors.New("could not call AfterResume RPC")
	ErrCouldNotCallBeforeSuspendRPC = errors.New("could not call BeforeSuspend RPC")
)

/**
 * Create a new snapshot
 *
 */
func CreateSnapshot(log types.Logger, ctx context.Context, devices []SnapshotDevice, ioEngineSync bool,
	vmConfiguration VMConfiguration, livenessConfiguration LivenessConfiguration,
	hypervisorConfiguration FirecrackerMachineConfig, networkConfiguration NetworkConfiguration,
	agentConfiguration AgentConfiguration) error {

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

	server, err := StartFirecrackerMachine(ctx, log, &hypervisorConfiguration)

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

	// Setup the liveness bits
	liveness := ipc.NewLivenessServer(filepath.Join(server.VMPath, VSockName), uint32(livenessConfiguration.LivenessVSockPort))

	livenessVSockPath, err := liveness.Open()
	if err != nil {
		return errors.Join(ErrCouldNotOpenLivenessServer, err)
	}

	if log != nil {
		log.Debug().Msg("Created liveness server")
	}

	err = os.Chown(livenessVSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID)
	if err != nil {
		return errors.Join(ErrCouldNotChownLivenessServerVSock, err)
	}
	defer liveness.Close()

	// Setup the agent server
	agent, err := ipc.StartAgentServer[struct{}, ipc.AgentServerRemote[struct{}]](
		log, filepath.Join(server.VMPath, VSockName), uint32(agentConfiguration.AgentVSockPort), struct{}{},
	)

	if err != nil {
		return errors.Join(ErrCouldNotStartAgentServer, err)
	}

	if log != nil {
		log.Debug().Msg("Created agent server")
	}

	err = os.Chown(agent.VSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID)
	if err != nil {
		return errors.Join(ErrCouldNotChownAgentServerVSock, err)
	}
	defer agent.Close()

	disks := []string{}
	for _, device := range devices {
		if strings.TrimSpace(device.Input) != "" {
			_, err := copyFile(device.Input, filepath.Join(server.VMPath, device.Name), hypervisorConfiguration.UID, hypervisorConfiguration.GID)
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

	err = server.StartVM(ctx, common.DeviceKernelName, disks, ioEngine,
		&vmConfiguration, &networkConfiguration, VSockName, ipc.VSockCIDGuest)

	if err != nil {
		return errors.Join(ErrCouldNotStartVM, err)
	}
	defer os.Remove(filepath.Join(server.VMPath, VSockName))

	if log != nil {
		log.Info().Msg("Started firecracker VM")
	}

	// Check for liveness
	receiveCtx, livenessCancel := context.WithTimeout(ctx, livenessConfiguration.ResumeTimeout)
	defer livenessCancel()

	err = liveness.ReceiveAndClose(receiveCtx)
	if err != nil {
		return errors.Join(ErrCouldNotReceiveAndCloseLivenessServer, err)
	}

	if log != nil {
		log.Debug().Msg("Liveness check OK")
	}
	liveness.Close()

	// BeforeSuspend and close
	var acceptingAgent *ipc.AgentConnection[struct{}, ipc.AgentServerRemote[struct{}], struct{}]
	acceptCtx, acceptCancel := context.WithTimeout(ctx, livenessConfiguration.ResumeTimeout)
	defer acceptCancel()

	acceptingAgentErr := make(chan error, 1)

	acceptingAgent, err = agent.Accept(acceptCtx, ctx,
		ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{}, acceptingAgentErr)

	if err != nil {
		return errors.Join(ErrCouldNotAcceptAgentConnection, err)
	}
	defer acceptingAgent.Close()

	if log != nil {
		log.Debug().Msg("Calling Remote BeforeSuspend")
	}

	err = acceptingAgent.Remote.BeforeSuspend(acceptCtx)
	if err != nil {
		return errors.Join(ErrCouldNotBeforeSuspend, err)
	}

	if log != nil {
		log.Debug().Msg("RPC Closing")
	}

	// Connections need to be closed before creating the snapshot
	err = acceptingAgent.Close()
	if err != nil {
		return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
	}
	agent.Close()

	// Check if there was any error
	select {
	case err := <-acceptingAgentErr:
		return err
	default:
	}

	err = createFinalSnapshot(ctx, server, agentConfiguration.AgentVSockPort,
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

	return nil
}

func copyFile(src, dst string, uid int, gid int) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, errors.Join(ErrCouldNotOpenSourceFile, err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return 0, errors.Join(ErrCouldNotCreateDestinationFile, err)
	}
	defer dstFile.Close()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return 0, errors.Join(ErrCouldNotCopyFileContent, err)
	}

	if err := os.Chown(dst, uid, gid); err != nil {
		return 0, errors.Join(ErrCouldNotChangeFileOwner, err)
	}

	return n, nil
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
func createFinalSnapshot(ctx context.Context, server *FirecrackerMachine, vsockPort uint32, vmPath string, uid int, gid int) error {
	err := server.CreateSnapshot(
		ctx,
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
