package roles

import (
	"context"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/network"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/loopholelabs/architekt/pkg/vsock"
	"github.com/loopholelabs/voltools/fsdata"
)

const (
	InitramfsName = "architekt.arkinitramfs"
	KernelName    = "architekt.arkkernel"
	DiskName      = "architekt.arkdisk"
)

type LivenessConfiguration struct {
	LivenessVSockPort uint32
}

type VMConfiguration struct {
	CpuCount           int
	MemorySize         int
	PackagePaddingSize int
}

type Packager struct {
	wg   sync.WaitGroup
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

	vmConfiguration VMConfiguration,
	livenessConfiguration LivenessConfiguration,

	hypervisorConfiguration HypervisorConfiguration,
	networkConfiguration NetworkConfiguration,
	agentConfiguration AgentConfiguration,
) error {
	p.wg = sync.WaitGroup{}
	p.errs = make(chan error)

	defer func() {
		p.wg.Wait()

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

	packageDir, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(packageDir)

	mount := utils.NewMount(devicePath, packageDir)

	if err := mount.Open(); err != nil {
		return err
	}
	defer mount.Close()

	ping := vsock.NewLivenessPingReceiver(
		filepath.Join(packageDir, VSockName),
		uint32(livenessConfiguration.LivenessVSockPort),
	)

	if err := ping.Open(); err != nil {
		return err
	}
	defer ping.Close()

	tap := network.NewTAP(
		networkConfiguration.HostInterface,
		networkConfiguration.HostMAC,
		networkConfiguration.BridgeInterface,
	)

	if err := tap.Open(); err != nil {
		return err
	}
	defer tap.Close()

	firecrackerSocketDir, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(firecrackerSocketDir)
	firecrackerSocketPath := filepath.Join(firecrackerSocketDir, "firecracker.sock")

	srv := firecracker.NewServer(
		hypervisorConfiguration.FirecrackerBin,
		firecrackerSocketPath,
		packageDir,
		hypervisorConfiguration.Verbose,
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
	if err := srv.Open(); err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", firecrackerSocketPath)
			},
		},
	}

	var (
		initramfsOutputPath = filepath.Join(packageDir, InitramfsName)
		kernelOutputPath    = filepath.Join(packageDir, KernelName)
		diskOutputPath      = filepath.Join(packageDir, DiskName)
	)

	if _, err := utils.CopyFile(initramfsInputPath, initramfsOutputPath); err != nil {
		return err
	}

	if _, err := utils.CopyFile(kernelInputPath, kernelOutputPath); err != nil {
		return err
	}

	if _, err := utils.CopyFile(diskInputPath, diskOutputPath); err != nil {
		return err
	}

	if err := firecracker.StartVM(
		client,

		InitramfsName,
		KernelName,
		DiskName,

		vmConfiguration.CpuCount,
		vmConfiguration.MemorySize,

		networkConfiguration.HostInterface,
		networkConfiguration.HostMAC,

		VSockName,
		vsock.CIDGuest,
	); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(packageDir, VSockName))

	if err := ping.Receive(); err != nil {
		return err
	}

	handler := vsock.NewHandler(
		filepath.Join(packageDir, VSockName),
		uint32(agentConfiguration.AgentVSockPort),

		time.Second*10,
	)

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := handler.Wait(); err != nil {
			p.errs <- err
		}
	}()

	defer handler.Close()
	peer, err := handler.Open(ctx, time.Millisecond*100, time.Second*10)
	if err != nil {
		return err
	}

	if err := peer.BeforeSuspend(ctx); err != nil {
		return err
	}

	// Connections need to be closed before creating the snapshot
	ping.Close()
	_ = handler.Close()

	if err := firecracker.CreateSnapshot(client); err != nil {
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

	r.wg.Wait()

	return nil
}
