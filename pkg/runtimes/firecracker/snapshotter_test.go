//go:build integration
// +build integration

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
	"github.com/loopholelabs/logging/types"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/stretchr/testify/assert"

	"github.com/loopholelabs/drafter/pkg/testutil"
)

const snapshotDir = "snap_test"
const blueprintDir = "../../../out/blueprint"

/**
 * Pre-requisites
 *  - firecracker works
 *  - blueprints exist
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

	ns := testutil.SetupNAT(t, "", "dra")

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(loggingtypes.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	netns, err := ns.ClaimNamespace()
	assert.NoError(t, err)

	template, err := GetCPUTemplate()
	assert.NoError(t, err)

	log.Info().Str("template", template).Msg("using cpu template")

	bootargs := DefaultBootArgsNoPVM
	ispvm, err := IsPVMHost()
	assert.NoError(t, err)
	if ispvm {
		bootargs = DefaultBootArgs
	}

	snapDir := setupSnapshot(t, log, ctx, netns, VMConfiguration{
		CPUCount:    1,
		MemorySize:  1024,
		CPUTemplate: template,
		BootArgs:    bootargs,
	},
	)

	// Check output

	for _, n := range []string{"state", "memory", "kernel", "disk", "config", "oci"} {
		s, err := os.Stat(path.Join(snapDir, n))
		assert.NoError(t, err)

		fmt.Printf("Output %s filesize %d\n", n, s.Size())
		assert.Greater(t, s.Size(), int64(0))
	}
}

/**
 * Pre-requisites
 *  - firecracker works
 *  - blueprints exist
 */
func setupSnapshot(t *testing.T, log types.Logger, ctx context.Context, netns string, vmConfiguration VMConfiguration) string {
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

	devices := []SnapshotDevice{
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

	err = CreateSnapshot(log, ctx, devices, false,
		vmConfiguration,
		LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  snapshotDir,
			UID:            0,
			GID:            0,
			NetNS:          netns,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         nil,
			Stderr:         nil,
			Stdin:          nil,
		},
		NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
		func() error { return nil })

	assert.NoError(t, err)

	return snapshotDir
}
