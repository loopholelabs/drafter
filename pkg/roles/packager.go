package roles

import (
	"archive/tar"
	"context"
	"encoding/json"
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

type Packager struct {
	errs chan error
}

func NewPackager() *Packager {
	return &Packager{}
}

func (p *Packager) CreatePackage(
	ctx context.Context,

	initramfsInputPath string,
	kernelInputPath string,
	diskInputPath string,

	packageOutputPath string,

	vmConfiguration config.VMConfiguration,
	livenessConfiguration config.LivenessConfiguration,

	hypervisorConfiguration config.HypervisorConfiguration,
	networkConfiguration config.NetworkConfiguration,
	agentConfiguration config.AgentConfiguration,

	packageConfiguration config.PackageConfiguration,
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

	packageOutputFile, err := os.OpenFile(packageOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer packageOutputFile.Close()

	packageOutputArchive := tar.NewWriter(packageOutputFile)
	defer packageOutputArchive.Close()

	defer func() {
		for _, name := range []string{
			packageConfiguration.InitramfsName,
			packageConfiguration.KernelName,
			packageConfiguration.DiskName,

			packageConfiguration.StateName,
			packageConfiguration.MemoryName,

			packageConfiguration.PackageConfigName,
		} {
			path := filepath.Join(vmPath, name)

			info, err := os.Stat(path)
			if err != nil {
				p.errs <- err

				return
			}

			if !(info.Mode().IsDir() || info.Mode().IsRegular()) {
				return
			}

			name, err := filepath.Rel(vmPath, path)
			if err != nil {
				p.errs <- err

				return
			}

			header, err := tar.FileInfoHeader(info, path)
			if err != nil {
				p.errs <- err

				return
			}
			header.Name = name

			if err := packageOutputArchive.WriteHeader(header); err != nil {
				p.errs <- err

				return
			}

			if !info.Mode().IsDir() {
				f, err := os.Open(path)
				if err != nil {
					p.errs <- err

					return
				}
				defer f.Close()

				if _, err = io.Copy(packageOutputArchive, f); err != nil {
					p.errs <- err

					return
				}
			}
		}
	}()

	defer srv.Close() // We need to stop the Firecracker process from using the mount before we can unmount it

	var (
		initramfsOutputPath     = filepath.Join(vmPath, packageConfiguration.InitramfsName)
		kernelOutputPath        = filepath.Join(vmPath, packageConfiguration.KernelName)
		diskOutputPath          = filepath.Join(vmPath, packageConfiguration.DiskName)
		packageConfigOutputPath = filepath.Join(vmPath, packageConfiguration.PackageConfigName)
	)

	if _, err := utils.CopyFile(initramfsInputPath, initramfsOutputPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if _, err := utils.CopyFile(kernelInputPath, kernelOutputPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if _, err := utils.CopyFile(diskInputPath, diskOutputPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	if err := firecracker.StartVM(
		client,

		packageConfiguration.InitramfsName,
		packageConfiguration.KernelName,
		packageConfiguration.DiskName,

		vmConfiguration.CpuCount,
		vmConfiguration.MemorySize,
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

		agentConfiguration.ResumeTimeout,
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

	if err := remote.BeforeSuspend(ctx); err != nil {
		return err
	}

	// Connections need to be closed before creating the snapshot
	ping.Close()
	_ = handler.Close()

	if err := firecracker.CreateSnapshot(
		client,

		packageConfiguration.StateName,
		packageConfiguration.MemoryName,
	); err != nil {
		return err
	}

	packageConfig, err := json.Marshal(utils.PackageConfig{
		AgentVSockPort: agentConfiguration.AgentVSockPort,
	})
	if err != nil {
		return err
	}

	if _, err := utils.WriteFile(packageConfig, packageConfigOutputPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	return nil
}

func (r *Packager) Wait() error {
	for err := range r.errs {
		if err != nil {
			return err
		}
	}

	return nil
}
