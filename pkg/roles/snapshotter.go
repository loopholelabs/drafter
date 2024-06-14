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
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get NBD device stat")
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
	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(err)
	}

	server, err := firecracker.StartFirecrackerServer(
		ctx,

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
		panic(err)
	}
	defer server.Close()
	defer os.RemoveAll(filepath.Dir(server.VMPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	handleGoroutinePanics(true, func() {
		if err := server.Wait(); err != nil {
			panic(err)
		}
	})

	liveness := ipc.NewLivenessServer(
		filepath.Join(server.VMPath, vsockName),
		uint32(livenessConfiguration.LivenessVSockPort),
	)

	livenessVSockPath, err := liveness.Open()
	if err != nil {
		panic(err)
	}
	defer liveness.Close()

	if err := os.Chown(livenessVSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	agent, err := ipc.StartAgentServer(
		filepath.Join(server.VMPath, vsockName),
		uint32(agentConfiguration.AgentVSockPort),
	)
	if err != nil {
		panic(err)
	}
	defer agent.Close()

	if err := os.Chown(agent.VSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(server.VMPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	defer func() {
		defer handlePanics(true)()

		if errs != nil {
			return
		}

		for _, device := range devices {
			inputFile, err := os.Open(filepath.Join(server.VMPath, device.Name))
			if err != nil {
				panic(err)
			}
			defer inputFile.Close()

			if err := os.MkdirAll(filepath.Dir(device.Output), os.ModePerm); err != nil {
				panic(err)
			}

			outputFile, err := os.OpenFile(device.Output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				panic(err)
			}
			defer outputFile.Close()

			deviceSize, err := io.Copy(outputFile, inputFile)
			if err != nil {
				panic(err)
			}

			if paddingLength := utils.GetBlockDevicePadding(deviceSize); paddingLength > 0 {
				if _, err := outputFile.Write(make([]byte, paddingLength)); err != nil {
					panic(err)
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
				panic(err)
			}
		}

		if !slices.Contains(KnownNames, device.Name) || device.Name == DiskName {
			disks = append(disks, device.Name)
		}
	}

	if err := firecracker.StartVM(
		ctx,

		client,

		KernelName,

		disks,

		vmConfiguration.CPUCount,
		vmConfiguration.MemorySize,
		vmConfiguration.CPUTemplate,
		vmConfiguration.BootArgs,

		networkConfiguration.Interface,
		networkConfiguration.MAC,

		vsockName,
		ipc.VSockCIDGuest,
	); err != nil {
		panic(err)
	}
	defer os.Remove(filepath.Join(server.VMPath, vsockName))

	{
		receiveCtx, cancel := context.WithTimeout(ctx, livenessConfiguration.ResumeTimeout)
		defer cancel()

		if err := liveness.ReceiveAndClose(receiveCtx); err != nil {
			panic(err)
		}
	}

	var acceptingAgent *ipc.AcceptingAgentServer
	{
		acceptCtx, cancel := context.WithTimeout(ctx, agentConfiguration.ResumeTimeout)
		defer cancel()

		acceptingAgent, err = agent.Accept(acceptCtx, ctx)
		if err != nil {
			panic(err)
		}
		defer acceptingAgent.Close()

		handleGoroutinePanics(true, func() {
			if err := acceptingAgent.Wait(); err != nil {
				panic(err)
			}
		})

		if err := acceptingAgent.Remote.BeforeSuspend(acceptCtx); err != nil {
			panic(err)
		}
	}

	// Connections need to be closed before creating the snapshot
	liveness.Close()
	if err := acceptingAgent.Close(); err != nil {
		panic(err)
	}
	agent.Close()

	if err := firecracker.CreateSnapshot(
		ctx,

		client,

		StateName,
		MemoryName,

		firecracker.SnapshotTypeFull,
	); err != nil {
		panic(err)
	}

	packageConfig, err := json.Marshal(PackageConfiguration{
		AgentVSockPort: agentConfiguration.AgentVSockPort,
	})
	if err != nil {
		panic(err)
	}

	outputFile, err := os.OpenFile(filepath.Join(server.VMPath, ConfigName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	if _, err := outputFile.Write(packageConfig); err != nil {
		panic(err)
	}

	if err := os.Chown(filepath.Join(server.VMPath, ConfigName), hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	return
}
