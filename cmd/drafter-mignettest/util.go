package main

import (
	"context"
	"os"
	"os/exec"
	"path"
	"time"

	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"

	"github.com/loopholelabs/logging/types"
)

/**
 * Pre-requisites
 *  - ark0 network namespace exists
 *  - firecracker works
 *  - blueprints exist
 */
func setupSnapshot(log types.Logger, ctx context.Context, snapDir string, blueDir string) error {
	err := os.Mkdir(snapDir, 0777)
	if err != nil {
		return err
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	devices := []rfirecracker.SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(snapDir, "state"),
		},
		{
			Name:   "memory",
			Output: path.Join(snapDir, "memory"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(blueDir, "vmlinux"),
			Output: path.Join(snapDir, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(blueDir, "rootfs.ext4"),
			Output: path.Join(snapDir, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(snapDir, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(blueDir, "oci.ext4"),
			Output: path.Join(snapDir, "oci"),
		},
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, true,
		rfirecracker.VMConfiguration{
			CPUCount:    1,
			MemorySize:  1024,
			CPUTemplate: cpuTemplate,
			BootArgs:    bootArgs,
		},
		rfirecracker.LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  snapDir,
			UID:            0,
			GID:            0,
			NetNS:          *networkNamespace,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         os.Stdout,
			Stderr:         os.Stderr,
			Stdin:          nil,
		},
		rfirecracker.NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		rfirecracker.AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
		func() {})

	return err
}
