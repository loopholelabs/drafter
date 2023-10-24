package main

import (
	"context"
	"flag"
	"os/exec"
	"path/filepath"

	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")
	iface := flag.String("interface", "tap0", "Name of the interface in the network namespace to use")
	mac := flag.String("mac", "02:0e:d9:fd:68:3d", "MAC of the interface in the network namespace to use")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	livenessVSockPort := flag.Int("liveness-vsock-port", 25, "Liveness VSock port")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join("out", "blueprint", "architekt.arkinitramfs"), "initramfs input path")
	kernelInputPath := flag.String("kernel-input-path", filepath.Join("out", "blueprint", "architekt.arkkernel"), "Kernel input path")
	diskInputPath := flag.String("disk-input-path", filepath.Join("out", "blueprint", "architekt.arkdisk"), "Disk input path")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")

	packageOutputPath := flag.String("package-output-path", filepath.Join("/tmp", "redis.ark"), "Path to write package file to")
	packagePaddingSize := flag.Int("package-padding-size", 128, "Padding to add to package for state file and file system metadata (in MB)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	packager := roles.NewPackager()

	go func() {
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

		utils.VMConfiguration{
			CpuCount:           *cpuCount,
			MemorySize:         *memorySize,
			PackagePaddingSize: *packagePaddingSize,
		},
		utils.LivenessConfiguration{
			LivenessVSockPort: uint32(*livenessVSockPort),
		},

		utils.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NetNS:         *netns,
			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		utils.NetworkConfiguration{
			Interface: *iface,
			MAC:       *mac,
		},
		utils.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
		},
	); err != nil {
		panic(err)
	}
}
