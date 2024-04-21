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
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/firecracker"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get NBD device stat")
)

type Snapshotter struct {
	errs chan error
}

func NewSnapshotter() *Snapshotter {
	return &Snapshotter{}
}

func (p *Snapshotter) CreateSnapshot(
	ctx context.Context,

	initramfsInputPath string,
	kernelInputPath string,
	diskInputPath string,

	stateOutputPath string,
	memoryOutputPath string,
	initramfsOutputPath string,
	kernelOutputPath string,
	diskOutputPath string,
	configOutputPath string,

	vmConfiguration config.VMConfiguration,
	livenessConfiguration config.LivenessConfiguration,

	hypervisorConfiguration config.HypervisorConfiguration,
	networkConfiguration config.NetworkConfiguration,
	agentConfiguration config.AgentConfiguration,

	knownNamesConfiguration config.KnownNamesConfiguration,
) error {
	p.errs = make(chan error)

	defer func() {
		close(p.errs)
	}()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		return err
	}

	srv := firecracker.NewServer(
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

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := srv.Wait(); err != nil {
			p.errs <- err
		}
	}()

	defer srv.Close()
	vmPath, err := srv.Open()
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(vmPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	ping := vsock.NewLivenessPingReceiver(
		filepath.Join(vmPath, VSockName),
		uint32(livenessConfiguration.LivenessVSockPort),
	)

	livenessVSockPath, err := ping.Open()
	if err != nil {
		return err
	}
	defer ping.Close()

	if err := os.Chown(livenessVSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(vmPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	defer func() {
		for _, resource := range [][2]string{
			{
				knownNamesConfiguration.InitramfsName,
				initramfsOutputPath,
			},
			{
				knownNamesConfiguration.KernelName,
				kernelOutputPath,
			},
			{
				knownNamesConfiguration.DiskName,
				diskOutputPath,
			},

			{
				knownNamesConfiguration.StateName,
				stateOutputPath,
			},
			{
				knownNamesConfiguration.MemoryName,
				memoryOutputPath,
			},

			{
				knownNamesConfiguration.ConfigName,
				configOutputPath,
			},
		} {
			inputFile, err := os.Open(filepath.Join(vmPath, resource[0]))
			if err != nil {
				p.errs <- err

				return
			}
			defer inputFile.Close()

			if err := os.MkdirAll(filepath.Dir(resource[1]), os.ModePerm); err != nil {
				p.errs <- err

				return
			}

			outputFile, err := os.OpenFile(resource[1], os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				p.errs <- err

				return
			}
			defer outputFile.Close()

			resourceSize, err := io.Copy(outputFile, inputFile)
			if err != nil {
				p.errs <- err

				return
			}

			if paddingLength := utils.GetBlockDevicePadding(resourceSize); paddingLength > 0 {
				if _, err := outputFile.Write(make([]byte, paddingLength)); err != nil {
					p.errs <- err

					return
				}
			}
		}
	}()

	defer srv.Close() // We need to stop the Firecracker process from using the mount before we can unmount it

	var (
		initramfsWorkingPath = filepath.Join(vmPath, knownNamesConfiguration.InitramfsName)
		kernelWorkingPath    = filepath.Join(vmPath, knownNamesConfiguration.KernelName)
		diskWorkingPath      = filepath.Join(vmPath, knownNamesConfiguration.DiskName)
	)

	if _, err := utils.CopyFile(initramfsInputPath, initramfsWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if _, err := utils.CopyFile(kernelInputPath, kernelWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if _, err := utils.CopyFile(diskInputPath, diskWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if err := firecracker.StartVM(
		client,

		knownNamesConfiguration.InitramfsName,
		knownNamesConfiguration.KernelName,
		knownNamesConfiguration.DiskName,

		vmConfiguration.CPUCount,
		vmConfiguration.MemorySize,
		vmConfiguration.CPUTemplate,
		vmConfiguration.BootArgs,

		networkConfiguration.Interface,
		networkConfiguration.MAC,

		VSockName,
		vsock.CIDGuest,
	); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(vmPath, VSockName))

	if err := ping.Receive(); err != nil {
		return err
	}

	handler := vsock.NewHandler(
		filepath.Join(vmPath, VSockName),
		uint32(agentConfiguration.AgentVSockPort),
	)

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := handler.Wait(); err != nil {
			p.errs <- err
		}
	}()

	defer handler.Close()
	remote, err := handler.Open(ctx, time.Millisecond*100, agentConfiguration.ResumeTimeout)
	if err != nil {
		return err
	}

	{
		ctx, cancel := context.WithTimeout(ctx, agentConfiguration.ResumeTimeout)
		defer cancel()

		if err := remote.BeforeSuspend(ctx); err != nil {
			return err
		}
	}

	// Connections need to be closed before creating the snapshot
	ping.Close()
	_ = handler.Close()

	if err := firecracker.CreateSnapshot(
		client,

		knownNamesConfiguration.StateName,
		knownNamesConfiguration.MemoryName,

		firecracker.SnapshotTypeFull,
	); err != nil {
		return err
	}

	packageConfig, err := json.Marshal(config.PackageConfiguration{
		AgentVSockPort: agentConfiguration.AgentVSockPort,
	})
	if err != nil {
		return err
	}

	if _, err := utils.WriteFile(packageConfig, filepath.Join(vmPath, knownNamesConfiguration.ConfigName), hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	return nil
}

func (r *Snapshotter) Wait() error {
	for err := range r.errs {
		if err != nil {
			return err
		}
	}

	return nil
}
