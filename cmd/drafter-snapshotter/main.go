package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
)

func main() {
	log := logging.New(logging.Zerolog, "drafter", os.Stderr)
	log.SetLevel(types.DebugLevel)

	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "chroot base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")
	inputKeepalive := flag.Bool("input-keepalive", true, "Whether to continously write backspace characters to the VM stdin to force the VM stdout to flush")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")
	iface := flag.String("interface", "tap0", "Name of the interface in the network namespace to use")
	mac := flag.String("mac", "02:0e:d9:fd:68:3d", "MAC of the interface in the network namespace to use")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	livenessVSockPort := flag.Int("liveness-vsock-port", 25, "Liveness VSock port")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	defDevices := make([]rfirecracker.SnapshotDevice, 0)
	for _, n := range common.KnownNames {
		sd := rfirecracker.SnapshotDevice{
			Name:   n,
			Output: filepath.Join("out", "package", common.DeviceFilenames[n]),
		}
		if n == common.DeviceKernelName ||
			n == common.DeviceDiskName ||
			n == common.DeviceOCIName {
			sd.Output = filepath.Join("out", "blueprint", common.DeviceFilenames[n])
		}
		defDevices = append(defDevices, sd)
	}
	defaultDevices, err := json.Marshal(defDevices)

	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")
	ioEngineSync := flag.Bool("io-engine-sync", false, "Whether to use the synchronous Firecracker IO engine instead of the default asynchronous one")

	cpuCount := flag.Int64("cpu-count", 1, "CPU count")
	memorySize := flag.Int64("memory-size", 1024, "Memory size (in MB)")
	cpuTemplate := flag.String("cpu-template", "None", "Firecracker CPU template (see https://github.com/firecracker-microvm/firecracker/blob/main/docs/cpu_templates/cpu-templates.md#static-cpu-templates for the options)")
	bootArgs := flag.String("boot-args", rfirecracker.DefaultBootArgs, "Boot/kernel arguments")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []rfirecracker.SnapshotDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	fcconfig := rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  *chrootBaseDir,
		UID:            *uid,
		GID:            *gid,
		NetNS:          *netns,
		NumaNode:       *numaNode,
		CgroupVersion:  *cgroupVersion,
		InputKeepalive: *inputKeepalive,
	}

	if *enableInput {
		fcconfig.Stdin = os.Stdin
	}

	if *enableOutput {
		fcconfig.Stdout = os.Stdout
		fcconfig.Stderr = os.Stderr
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, *ioEngineSync,
		rfirecracker.VMConfiguration{
			CPUCount:    *cpuCount,
			MemorySize:  *memorySize,
			CPUTemplate: *cpuTemplate,
			BootArgs:    *bootArgs,
		},
		rfirecracker.LivenessConfiguration{
			LivenessVSockPort: uint32(*livenessVSockPort),
			ResumeTimeout:     *resumeTimeout,
		},

		fcconfig,
		rfirecracker.NetworkConfiguration{
			Interface: *iface,
			MAC:       *mac,
		},
		rfirecracker.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
			ResumeTimeout:  *resumeTimeout,
		},
		func() {})

	if err != nil {
		panic(err)
	}

	log.Info().Msg("Shutting down")
}
