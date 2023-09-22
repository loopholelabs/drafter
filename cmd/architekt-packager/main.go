package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/architekt/pkg/roles"
)

func main() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	livenessVSockPort := flag.Int("liveness-vsock-port", 25, "Liveness VSock port")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkinitramfs"), "initramfs input path")
	kernelInputPath := flag.String("kernel-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkkernel"), "Kernel input path")
	diskInputPath := flag.String("disk-input-path", filepath.Join(pwd, "out", "blueprint", "architekt.arkdisk"), "Disk input path")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")

	packageOutputPath := flag.String("package-output-path", filepath.Join("out", "redis.ark"), "Path to write package file to")
	packagePaddingSize := flag.Int("package-padding-size", 128, "Padding to add to package for state file and file system metadata (in MB)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	packager := roles.NewPackager()

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := packager.Wait(); err != nil {
			panic(err)
		}
	}()

	if err := packager.CreatePackage(
		ctx,

		*initramfsInputPath,
		*kernelInputPath,
		*diskInputPath,

		*packageOutputPath,

		roles.VMConfiguration{
			CpuCount:           *cpuCount,
			MemorySize:         *memorySize,
			PackagePaddingSize: *packagePaddingSize,
		},
		roles.LivenessConfiguration{
			LivenessVSockPort: uint32(*livenessVSockPort),
		},

		roles.HypervisorConfiguration{
			FirecrackerBin: *firecrackerBin,

			Verbose:      *verbose,
			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		roles.NetworkConfiguration{
			HostInterface:   *hostInterface,
			HostMAC:         *hostMAC,
			BridgeInterface: *bridgeInterface,
		},
		roles.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
		},
	); err != nil {
		panic(err)
	}

	wg.Wait()
}
