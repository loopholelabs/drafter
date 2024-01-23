package main

import (
	"context"
	"flag"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")
	iface := flag.String("interface", "tap0", "Name of the interface in the network namespace to use")
	mac := flag.String("mac", "02:0e:d9:fd:68:3d", "MAC of the interface in the network namespace to use")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	livenessVSockPort := flag.Int("liveness-vsock-port", 25, "Liveness VSock port")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	initramfsInputPath := flag.String("initramfs-input-path", filepath.Join("out", "blueprint", "drafter.drftinitramfs"), "initramfs input path")
	kernelInputPath := flag.String("kernel-input-path", filepath.Join("out", "blueprint", "drafter.drftkernel"), "Kernel input path")
	diskInputPath := flag.String("disk-input-path", filepath.Join("out", "blueprint", "drafter.drftdisk"), "Disk input path")

	stateOutputPath := flag.String("state-output-path", filepath.Join("out", "package", "drafter.drftstate"), "State output path")
	memoryOutputPath := flag.String("memory-output-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory output path")
	initramfsOutputPath := flag.String("initramfs-output-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs output path")
	kernelOutputPath := flag.String("kernel-output-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel output path")
	diskOutputPath := flag.String("disk-output-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk output path")
	configOutputPath := flag.String("config-output-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config output path")

	cpuCount := flag.Int("cpu-count", 1, "CPU count")
	memorySize := flag.Int("memory-size", 1024, "Memory size (in MB)")
	bootArgs := flag.String("boot-args", config.DefaultBootArgs, "Boot/kernel arguments")

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

	snapshotter := roles.NewSnapshotter()

	go func() {
		if err := snapshotter.Wait(); err != nil {
			panic(err)
		}
	}()

	if err := snapshotter.CreateSnapshot(
		ctx,

		*initramfsInputPath,
		*kernelInputPath,
		*diskInputPath,

		*stateOutputPath,
		*memoryOutputPath,
		*initramfsOutputPath,
		*kernelOutputPath,
		*diskOutputPath,
		*configOutputPath,

		config.VMConfiguration{
			CpuCount:   *cpuCount,
			MemorySize: *memorySize,
			BootArgs:   *bootArgs,
		},
		config.LivenessConfiguration{
			LivenessVSockPort: uint32(*livenessVSockPort),
		},

		config.HypervisorConfiguration{
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
		config.NetworkConfiguration{
			Interface: *iface,
			MAC:       *mac,
		},
		config.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
			ResumeTimeout:  *resumeTimeout,
		},

		config.KnownNamesConfiguration{
			InitramfsName: config.InitramfsName,
			KernelName:    config.KernelName,
			DiskName:      config.DiskName,

			StateName:  config.StateName,
			MemoryName: config.MemoryName,

			ConfigName: config.ConfigName,
		},
	); err != nil {
		panic(err)
	}
}
