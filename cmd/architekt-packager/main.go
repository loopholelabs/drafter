package main

import (
	"context"
	"flag"
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
	initramfsName = "architekt.arkinitramfs"
	kernelName    = "architekt.arkkernel"
	diskName      = "architekt.arkdisk"
)

func main() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	firecrackerSocketPath := flag.String("firecracker-socket-path", filepath.Join(pwd, "firecracker.sock"), "Firecracker socket path (must be absolute)")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	vsockPath := flag.String("vsock-path", "vsock.sock", "VSock path")
	livenessVSockPort := flag.Int("liveness-vsock-port", 25, "Liveness VSock port")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkinitramfs"), "initramfs input path")
	kernelInputPath := flag.String("kernel-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkkernel"), "Kernel input path")
	diskInputPath := flag.String("disk-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkdisk"), "Disk input path")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")

	packagePath := flag.String("package-path", filepath.Join("out", "redis.ark"), "Path to write package file to")
	packagePaddingSize := flag.Int("package-padding-size", 128, "Padding to add to package for state file and file system metadata (in MB)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initramfsSize, err := utils.GetFileSize(*initramfsInputPath)
	if err != nil {
		panic(err)
	}

	kernelSize, err := utils.GetFileSize(*kernelInputPath)
	if err != nil {
		panic(err)
	}

	diskSize, err := utils.GetFileSize(*diskInputPath)
	if err != nil {
		panic(err)
	}

	packageSize := math.Ceil((float64(((initramfsSize+kernelSize+diskSize)/(1024*1024))+int64(*memorySize)+int64(*packagePaddingSize))/float64(1024))/float64(10)) * 10

	if err := fsdata.CreateFile(int(packageSize), *packagePath); err != nil {
		panic(err)
	}

	packageDir, err := os.MkdirTemp("", "")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(packageDir)

	mount := utils.NewLoopMount(*packagePath, packageDir)

	if err := mount.Open(); err != nil {
		panic(err)
	}
	defer mount.Close()

	ping := vsock.NewLivenessPingReceiver(
		filepath.Join(packageDir, *vsockPath),
		uint32(*livenessVSockPort),
	)

	if err := ping.Open(); err != nil {
		panic(err)
	}
	defer ping.Close()

	tap := network.NewTAP(
		*hostInterface,
		*hostMAC,
		*bridgeInterface,
	)

	if err := tap.Open(); err != nil {
		panic(err)
	}
	defer tap.Close()

	srv := firecracker.NewServer(
		*firecrackerBin,
		*firecrackerSocketPath,
		packageDir,

		*verbose,
		*enableOutput,
		*enableInput,
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := srv.Wait(); err != nil {
			panic(err)
		}
	}()

	defer srv.Close()
	if err := srv.Open(); err != nil {
		panic(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *firecrackerSocketPath)
			},
		},
	}

	var (
		initramfsOutputPath = filepath.Join(packageDir, initramfsName)
		kernelOutputPath    = filepath.Join(packageDir, kernelName)
		diskOutputPath      = filepath.Join(packageDir, diskName)
	)

	if _, err := utils.CopyFile(*initramfsInputPath, initramfsOutputPath); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(*kernelInputPath, kernelOutputPath); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(*diskInputPath, diskOutputPath); err != nil {
		panic(err)
	}

	if err := firecracker.StartVM(
		client,

		initramfsName,
		kernelName,
		diskName,

		*cpuCount,
		*memorySize,

		*hostInterface,
		*hostMAC,

		*vsockPath,
		vsock.CIDGuest,
	); err != nil {
		panic(err)
	}
	defer os.Remove(filepath.Join(packageDir, *vsockPath))

	if err := ping.Receive(); err != nil {
		panic(err)
	}

	handler := vsock.NewHandler(
		filepath.Join(packageDir, *vsockPath),
		uint32(*agentVSockPort),

		time.Second*10,
	)

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := handler.Wait(); err != nil {
			panic(err)
		}
	}()

	defer handler.Close()
	peer, err := handler.Open(ctx, time.Millisecond*100, time.Second*10)
	if err != nil {
		panic(err)
	}

	if err := peer.BeforeSuspend(ctx); err != nil {
		panic(err)
	}
	// Connections need to be closed before creating the snapshot
	ping.Close()
	_ = handler.Close()

	if err := firecracker.CreateSnapshot(client); err != nil {
		panic(err)
	}
}
