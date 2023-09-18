package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/liveness"
	"github.com/loopholelabs/architekt/pkg/network"
	"github.com/loopholelabs/architekt/pkg/utils"
)

const (
	initramfsName = "architekt.initramfs"
	kernelName    = "architekt.kernel"
	diskName      = "architekt.disk"
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
	vsockCID := flag.Int("vsock-cid", 3, "VSock CID")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join(pwd, "out", "template", "architekt.initramfs"), "initramfs input path")
	kernelInputPath := flag.String("kernel-input-path", filepath.Join(pwd, "out", "template", "architekt.kernel"), "Kernel input path")
	diskInputPath := flag.String("disk-input-path", filepath.Join(pwd, "out", "template", "architekt.disk"), "Disk input path")

	packagePath := flag.String("package-path", filepath.Join("out", "package"), "Path to write extracted package to")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")

	flag.Parse()

	if err := os.MkdirAll(*packagePath, os.ModePerm); err != nil {
		panic(err)
	}

	ping := liveness.NewLivenessPingReceiver(
		filepath.Join(*packagePath, *vsockPath),
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
		*packagePath,

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

	if err := srv.Start(); err != nil {
		panic(err)
	}
	defer srv.Stop()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *firecrackerSocketPath)
			},
		},
	}

	var (
		initramfsOutputPath = filepath.Join(*packagePath, initramfsName)
		kernelOutputPath    = filepath.Join(*packagePath, kernelName)
		diskOutputPath      = filepath.Join(*packagePath, diskName)
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
		*vsockCID,
	); err != nil {
		panic(err)
	}
	defer os.Remove(filepath.Join(*packagePath, *vsockPath))

	if err := ping.Receive(); err != nil {
		panic(err)
	}

	if err := firecracker.CreateSnapshot(client); err != nil {
		panic(err)
	}
}
