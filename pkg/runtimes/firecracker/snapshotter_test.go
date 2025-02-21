package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"testing"
	"time"

	"github.com/loopholelabs/logging"
	"github.com/stretchr/testify/assert"
)

const testDir = "snap_test"
const blueprintDir = "../../../image/blueprint"

/**
 * Pre-requisites
 *  - ark0 network namespace exists
 *  - firecracker works
 */
func TestSnapshotter(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	if currentUser.Username != "root" {
		fmt.Printf("Cannot run test unless we are root.\n")
		return
	}

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = os.Mkdir(testDir, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(testDir)
	})

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	devices := []SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(testDir, "state.bin"),
		},
		{
			Name:   "memory",
			Output: path.Join(testDir, "memory.bin"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(blueprintDir, "vmlinux"),
			Output: path.Join(testDir, "vmlinux"),
		},
		{
			Name:   "disk",
			Input:  path.Join(blueprintDir, "rootfs.ext4"),
			Output: path.Join(testDir, "rootfs.ext4"),
		},
		{
			Name:   "config",
			Output: path.Join(testDir, "config.json"),
		},
		{
			Name:   "oci",
			Input:  path.Join(blueprintDir, "oci.ext4"),
			Output: path.Join(testDir, "oci.ext4"),
		},
	}

	err = CreateSnapshot(log, ctx, devices,
		VMConfiguration{
			CPUCount:    1,
			MemorySize:  1024,
			CPUTemplate: "None",
			BootArgs:    DefaultBootArgsNoPVM,
		},
		LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  testDir,
			UID:            0,
			GID:            0,
			NetNS:          "ark0",
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   false,
			EnableInput:    false,
		},
		NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
	)

	assert.NoError(t, err)

	// Check output

	for _, n := range []string{"state.bin", "memory.bin", "vmlinux", "rootfs.ext4", "config.json", "oci.ext4"} {
		s, err := os.Stat(path.Join(testDir, n))
		assert.NoError(t, err)

		fmt.Printf("Output %s filesize %d\n", n, s.Size())
		assert.Greater(t, s.Size(), int64(0))
	}
}
