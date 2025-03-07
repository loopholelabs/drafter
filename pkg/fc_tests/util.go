package fc_tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"testing"
	"time"

	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"

	"github.com/loopholelabs/logging/types"
	"github.com/stretchr/testify/assert"
)

const snapshotDir = "snap_test"
const blueprintDir = "../../out/blueprint"

/**
 * Pre-requisites
 *  - ark0 network namespace exists
 *  - firecracker works
 *  - blueprints exist
 */
func setupSnapshot(t *testing.T, log types.Logger, ctx context.Context) string {
	currentUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	if currentUser.Username != "root" {
		fmt.Printf("Cannot run test unless we are root.\n")
		return ""
	}

	err = os.Mkdir(snapshotDir, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(snapshotDir)
	})

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	devices := []rfirecracker.SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(snapshotDir, "state"),
		},
		{
			Name:   "memory",
			Output: path.Join(snapshotDir, "memory"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(blueprintDir, "vmlinux"),
			Output: path.Join(snapshotDir, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(blueprintDir, "rootfs.ext4"),
			Output: path.Join(snapshotDir, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(snapshotDir, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(blueprintDir, "oci.ext4"),
			Output: path.Join(snapshotDir, "oci"),
		},
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, "Async",
		rfirecracker.VMConfiguration{
			CPUCount:    1,
			MemorySize:  1024,
			CPUTemplate: "None",
			BootArgs:    rfirecracker.DefaultBootArgsNoPVM,
		},
		rfirecracker.LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		rfirecracker.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  snapshotDir,
			UID:            0,
			GID:            0,
			NetNS:          "ark0",
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   false,
			EnableInput:    false,
		},
		rfirecracker.NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		rfirecracker.AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
	)

	assert.NoError(t, err)

	return snapshotDir
}
