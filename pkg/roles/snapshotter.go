package roles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/internal/firecracker"
	iutils "github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

var (
	ErrCouldNotGetDeviceStat                 = errors.New("could not get NBD device stat")
	ErrCouldNotWaitForFirecrackerServer      = errors.New("could not wait for Firecracker server")
	ErrCouldNotOpenLivenessServer            = errors.New("could not open liveness server")
	ErrCouldNotChownLivenessServerVSock      = errors.New("could not change ownership of liveness server VSock")
	ErrCouldNotChownAgentServerVSock         = errors.New("could not change ownership of agent server VSock")
	ErrCouldNotOpenInputFile                 = errors.New("could not open input file")
	ErrCouldNotCreateOutputFile              = errors.New("could not create output file")
	ErrCouldNotCopyFile                      = errors.New("error copying file")
	ErrCouldNotWritePadding                  = errors.New("could not write padding")
	ErrCouldNotCopyDeviceFile                = errors.New("could not copy device file")
	ErrCouldNotStartVM                       = errors.New("could not start VM")
	ErrCouldNotReceiveAndCloseLivenessServer = errors.New("could not receive and close liveness server")
	ErrCouldNotAcceptAgentConnection         = errors.New("could not accept agent connection")
	ErrCouldNotBeforeSuspend                 = errors.New("error before suspend")
	ErrCouldNotMarshalPackageConfig          = errors.New("could not marshal package configuration")
	ErrCouldNotOpenPackageConfigFile         = errors.New("could not open package configuration file")
	ErrCouldNotWritePackageConfig            = errors.New("could not write package configuration")
	ErrCouldNotChownPackageConfigFile        = errors.New("could not change ownership of package configuration file")
)

type AgentConfiguration struct {
	AgentVSockPort uint32
	ResumeTimeout  time.Duration
}

type LivenessConfiguration struct {
	LivenessVSockPort uint32
	ResumeTimeout     time.Duration
}

type SnapshotDevice struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

func CreateSnapshot(
	ctx context.Context,

	devices []SnapshotDevice,

	vmConfiguration VMConfiguration,
	livenessConfiguration LivenessConfiguration,

	hypervisorConfiguration HypervisorConfiguration,
	networkConfiguration NetworkConfiguration,
	agentConfiguration AgentConfiguration,
) (errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(errors.Join(ErrCouldNotCreateChrootBaseDirectory, err))
	}

	server, err := firecracker.StartFirecrackerServer(
		goroutineManager.Context(),

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
		panic(errors.Join(ErrCouldNotStartFirecrackerServer, err))
	}
	defer server.Close()
	defer os.RemoveAll(filepath.Dir(server.VMPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := server.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForFirecrackerServer, err))
		}
	})

	liveness := ipc.NewLivenessServer(
		filepath.Join(server.VMPath, VSockName),
		uint32(livenessConfiguration.LivenessVSockPort),
	)

	livenessVSockPath, err := liveness.Open()
	if err != nil {
		panic(errors.Join(ErrCouldNotOpenLivenessServer, err))
	}
	defer liveness.Close()

	if err := os.Chown(livenessVSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(errors.Join(ErrCouldNotChownLivenessServerVSock, err))
	}

	agent, err := ipc.StartAgentServer(
		filepath.Join(server.VMPath, VSockName),
		uint32(agentConfiguration.AgentVSockPort),
	)
	if err != nil {
		panic(errors.Join(ErrCouldNotStartAgentServer, err))
	}
	defer agent.Close()

	if err := os.Chown(agent.VSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(errors.Join(ErrCouldNotChownAgentServerVSock, err))
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(server.VMPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if errs != nil {
			return
		}

		for _, device := range devices {
			inputFile, err := os.Open(filepath.Join(server.VMPath, device.Name))
			if err != nil {
				panic(errors.Join(ErrCouldNotOpenInputFile, err))
			}
			defer inputFile.Close()

			if err := os.MkdirAll(filepath.Dir(device.Output), os.ModePerm); err != nil {
				panic(errors.Join(ErrCouldNotCreateOutputDir, err))
			}

			outputFile, err := os.OpenFile(device.Output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				panic(errors.Join(ErrCouldNotCreateOutputFile, err))
			}
			defer outputFile.Close()

			deviceSize, err := io.Copy(outputFile, inputFile)
			if err != nil {
				panic(errors.Join(ErrCouldNotCopyFile, err))
			}

			if paddingLength := utils.GetBlockDevicePadding(deviceSize); paddingLength > 0 {
				if _, err := outputFile.Write(make([]byte, paddingLength)); err != nil {
					panic(errors.Join(ErrCouldNotWritePadding, err))
				}
			}
		}
	}()
	// We need to stop the Firecracker process from using the mount before we can unmount it
	defer server.Close()

	disks := []string{}
	for _, device := range devices {
		if strings.TrimSpace(device.Input) != "" {
			if _, err := iutils.CopyFile(device.Input, filepath.Join(server.VMPath, device.Name), hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
				panic(errors.Join(ErrCouldNotCopyDeviceFile, err))
			}
		}

		if !slices.Contains(KnownNames, device.Name) || device.Name == DiskName {
			disks = append(disks, device.Name)
		}
	}

	if err := firecracker.StartVM(
		goroutineManager.Context(),

		client,

		KernelName,

		disks,

		vmConfiguration.CPUCount,
		vmConfiguration.MemorySize,
		vmConfiguration.CPUTemplate,
		vmConfiguration.BootArgs,

		networkConfiguration.Interface,
		networkConfiguration.MAC,

		VSockName,
		ipc.VSockCIDGuest,
	); err != nil {
		panic(errors.Join(ErrCouldNotStartVM, err))
	}
	defer os.Remove(filepath.Join(server.VMPath, VSockName))

	{
		receiveCtx, cancel := context.WithTimeout(goroutineManager.Context(), livenessConfiguration.ResumeTimeout)
		defer cancel()

		if err := liveness.ReceiveAndClose(receiveCtx); err != nil {
			panic(errors.Join(ErrCouldNotReceiveAndCloseLivenessServer, err))
		}
	}

	var acceptingAgent *ipc.AcceptingAgentServer
	{
		acceptCtx, cancel := context.WithTimeout(goroutineManager.Context(), agentConfiguration.ResumeTimeout)
		defer cancel()

		acceptingAgent, err = agent.Accept(acceptCtx, goroutineManager.Context())
		if err != nil {
			panic(errors.Join(ErrCouldNotAcceptAgentConnection, err))
		}
		defer acceptingAgent.Close()

		goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
			if err := acceptingAgent.Wait(); err != nil {
				panic(errors.Join(ErrCouldNotWaitForAcceptingAgent, err))
			}
		})

		if err := acceptingAgent.Remote.BeforeSuspend(acceptCtx); err != nil {
			panic(errors.Join(ErrCouldNotBeforeSuspend, err))
		}
	}

	// Connections need to be closed before creating the snapshot
	liveness.Close()
	if err := acceptingAgent.Close(); err != nil {
		panic(errors.Join(ErrCouldNotCloseAcceptingAgent, err))
	}
	agent.Close()

	if err := firecracker.CreateSnapshot(
		goroutineManager.Context(),

		client,

		StateName,
		MemoryName,

		firecracker.SnapshotTypeFull,
	); err != nil {
		panic(errors.Join(ErrCouldNotCreateSnapshot, err))
	}

	packageConfig, err := json.Marshal(PackageConfiguration{
		AgentVSockPort: agentConfiguration.AgentVSockPort,
	})
	if err != nil {
		panic(errors.Join(ErrCouldNotMarshalPackageConfig, err))
	}

	outputFile, err := os.OpenFile(filepath.Join(server.VMPath, ConfigName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		panic(errors.Join(ErrCouldNotOpenPackageConfigFile, err))
	}
	defer outputFile.Close()

	if _, err := outputFile.Write(packageConfig); err != nil {
		panic(errors.Join(ErrCouldNotWritePackageConfig, err))
	}

	if err := os.Chown(filepath.Join(server.VMPath, ConfigName), hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(errors.Join(ErrCouldNotChownPackageConfigFile, err))
	}

	return
}
