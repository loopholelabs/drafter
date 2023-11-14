package roles

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/config"
	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/loopholelabs/architekt/pkg/vsock"
)

const (
	InitramfsName = "architekt.arkinitramfs"
	KernelName    = "architekt.arkkernel"
	DiskName      = "architekt.arkdisk"
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

	mountDir := filepath.Join(vmPath, firecracker.MountName)
	if err := os.MkdirAll(mountDir, os.ModePerm); err != nil {
		return err
	}

	packageOutputFile, err := os.OpenFile(packageOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer packageOutputFile.Close()

	packageOutputArchive := tar.NewWriter(packageOutputFile)
	defer packageOutputArchive.Close()

	defer func() {
		if err := filepath.Walk(mountDir, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}

			name, err := filepath.Rel(mountDir, path)
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(info, path)
			if err != nil {
				return err
			}
			header.Name = name

			if err := packageOutputArchive.WriteHeader(header); err != nil {
				return err
			}

			if !info.Mode().IsDir() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()

				if _, err = io.Copy(packageOutputArchive, f); err != nil {
					return err
				}
			}

			return nil
		}); err != nil {
			p.errs <- err
		}
	}()

	defer srv.Close() // We need to stop the Firecracker process from using the mount before we can unmount it

	if err := os.Chown(mountDir, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		return err
	}

	var (
		initramfsOutputPath     = filepath.Join(vmPath, firecracker.MountName, InitramfsName)
		kernelOutputPath        = filepath.Join(vmPath, firecracker.MountName, KernelName)
		diskOutputPath          = filepath.Join(vmPath, firecracker.MountName, DiskName)
		packageConfigOutputPath = filepath.Join(vmPath, firecracker.MountName, utils.PackageConfigName)
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

		filepath.Join(firecracker.MountName, InitramfsName),
		filepath.Join(firecracker.MountName, KernelName),
		filepath.Join(firecracker.MountName, DiskName),

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

	if err := firecracker.CreateSnapshot(client); err != nil {
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
