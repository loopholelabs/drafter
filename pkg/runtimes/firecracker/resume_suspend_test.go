package firecracker

import (
	"context"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"

	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/stretchr/testify/assert"
)

const resumeTestDir = "resume_suspend_test"

func TestResumeSuspend(t *testing.T) {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.DebugLevel)

	err := os.Mkdir(resumeTestDir, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(resumeTestDir)
	})

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := StartRunner(log, ctx, ctx, HypervisorConfiguration{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  resumeTestDir,
		UID:            0,
		GID:            0,
		NetNS:          "ark0",
		NumaNode:       0,
		CgroupVersion:  2,
		EnableOutput:   false,
		EnableInput:    false,
	})
	assert.NoError(t, err)

	devices := []SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(r.VMPath, "state"),
		},
		{
			Name:   "memory",
			Output: path.Join(r.VMPath, "memory"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(blueprintDir, "vmlinux"),
			Output: path.Join(r.VMPath, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(blueprintDir, "rootfs.ext4"),
			Output: path.Join(r.VMPath, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(r.VMPath, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(blueprintDir, "oci.ext4"),
			Output: path.Join(r.VMPath, "oci"),
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

	agentVsockPort := uint32(26)
	agentLocal := struct{}{}
	agentHooks := ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{}
	snapConfig := SnapshotLoadConfiguration{
		ExperimentalMapPrivate: false,
	}

	rr, err := Resume[struct{}, ipc.AgentServerRemote[struct{}], struct{}](r, ctx, 30*time.Second, 30*time.Second,
		agentVsockPort, agentLocal, agentHooks, snapConfig)
	assert.NoError(t, err)

	assert.NotNil(t, rr)

	err = rr.SuspendAndCloseAgentServer(ctx, 10*time.Second)
	assert.NoError(t, err)

	err = r.Close()
	assert.NoError(t, err)
}
