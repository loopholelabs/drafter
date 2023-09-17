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

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	firecrackerSocketPath := flag.String("firecracker-socket-path", "firecracker.sock", "Firecracker socket path")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	vsockPath := flag.String("vsock-path", "vsock.sock", "VSock path")
	vsockPort := flag.Int("vsock-port", 25, "VSock port")
	vsockCID := flag.Int("vsock-cid", 3, "VSock CID")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join("out", "template", "architekt.initramfs"), "initramfs input path")
	initramfsOutputPath := flag.String("initramfs-output-path", filepath.Join("out", "image", "architekt.initramfs"), "initramfs output path")

	kernelInputPath := flag.String("kernel-input-path", filepath.Join("out", "template", "architekt.kernel"), "Kernel input path")
	kernelOutputPath := flag.String("kernel-output-path", filepath.Join("out", "image", "architekt.kernel"), "Kernel output path")

	diskInputPath := flag.String("disk-input-path", filepath.Join("out", "template", "architekt.disk"), "Disk input path")
	diskOutputPath := flag.String("disk-output-path", filepath.Join("out", "image", "architekt.disk"), "Disk output path")

	stateOutputPath := flag.String("state-output-path", filepath.Join("out", "image", "architekt.state"), "State output path")
	memoryOutputPath := flag.String("memory-output-path", filepath.Join("out", "image", "architekt.memory"), "Memory output path")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")

	flag.Parse()

	ping := liveness.NewLivenessPingReceiver(
		*vsockPath,
		uint32(*vsockPort),
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

	if err := os.MkdirAll(filepath.Dir(*initramfsOutputPath), os.ModePerm); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(*initramfsInputPath, *initramfsOutputPath); err != nil {
		panic(err)
	}

	if err := os.MkdirAll(filepath.Dir(*kernelOutputPath), os.ModePerm); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(*kernelInputPath, *kernelOutputPath); err != nil {
		panic(err)
	}

	if err := os.MkdirAll(filepath.Dir(*diskOutputPath), os.ModePerm); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(*diskInputPath, *diskOutputPath); err != nil {
		panic(err)
	}

	if err := firecracker.StartVM(
		client,

		*initramfsOutputPath,
		*kernelOutputPath,
		*diskOutputPath,

		*cpuCount,
		*memorySize,

		*hostInterface,
		*hostMAC,

		*vsockPath,
		*vsockCID,
	); err != nil {
		panic(err)
	}
	defer os.Remove(*vsockPath)

	if err := ping.Receive(); err != nil {
		panic(err)
	}

	if err := os.MkdirAll(filepath.Dir(*stateOutputPath), os.ModePerm); err != nil {
		panic(err)
	}

	if err := os.MkdirAll(filepath.Dir(*memoryOutputPath), os.ModePerm); err != nil {
		panic(err)
	}

	if err := firecracker.CreateSnapshot(
		client,

		*stateOutputPath,
		*memoryOutputPath,
	); err != nil {
		panic(err)
	}
}
