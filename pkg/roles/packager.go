package roles

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/loopholelabs/architekt/pkg/vsock"
	"github.com/loopholelabs/voltools/fsdata"
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

	vmConfiguration utils.VMConfiguration,
	livenessConfiguration utils.LivenessConfiguration,

	hypervisorConfiguration utils.HypervisorConfiguration,
	networkConfiguration utils.NetworkConfiguration,
	agentConfiguration utils.AgentConfiguration,
) error {
	p.errs = make(chan error)

	defer func() {
		close(p.errs)
	}()

	initramfsSize, err := utils.GetFileSize(initramfsInputPath)
	if err != nil {
		return err
	}

	kernelSize, err := utils.GetFileSize(kernelInputPath)
	if err != nil {
		return err
	}

	diskSize, err := utils.GetFileSize(diskInputPath)
	if err != nil {
		return err
	}

	packageSize := math.Ceil((float64(((initramfsSize+kernelSize+diskSize)/(1024*1024))+int64(vmConfiguration.MemorySize)+int64(vmConfiguration.PackagePaddingSize))/float64(1024))/float64(10)) * 10

	if err := fsdata.CreateFile(int(packageSize), packageOutputPath); err != nil {
		return err
	}

	loop := utils.NewLoop(packageOutputPath)

	devicePath, err := loop.Open()
	if err != nil {
		return err
	}
	defer loop.Close()

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

	mount := utils.NewMount(devicePath, mountDir)

	if err := mount.Open(); err != nil {
		return err
	}
	defer mount.Close()
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
