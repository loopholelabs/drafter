//go:build integration
// +build integration

package fc_tests

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snapDir := setupSnapshot(t, log, ctx)

	agentVsockPort := uint32(26)
	agentLocal := struct{}{}
	agentHooks := ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{}
	snapConfig := rfirecracker.SnapshotLoadConfiguration{
		ExperimentalMapPrivate: false,
	}

	deviceFiles := []string{
		"state", "memory", "kernel", "disk", "config", "oci",
	}

	devicesAt := snapDir
	var previousRunner *rfirecracker.Runner

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	// Resume and suspend the vm a few times
	for n := 0; n < 10; n++ {
		r, err := rfirecracker.StartRunner(log, ctx, ctx, rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  resumeTestDir,
			UID:            0,
			GID:            0,
			NetNS:          "ark0",
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   true,
			EnableInput:    false,
		})
		assert.NoError(t, err)

		// Copy the devices in from the last place...
		for _, d := range deviceFiles {
			src := path.Join(devicesAt, d)
			dst := path.Join(r.VMPath, d)
			hash := calcHash(src)
			os.Link(src, dst)
			fmt.Printf("Device hash %x (%s)\n", hash, d)
		}
		devicesAt = r.VMPath

		// Now we can close the previous runner
		if previousRunner != nil {
			previousRunner.Close()
			assert.NoError(t, err)
		}

		rr, err := rfirecracker.Resume[struct{}, ipc.AgentServerRemote[struct{}], struct{}](r, ctx, 30*time.Second, 30*time.Second,
			agentVsockPort, agentLocal, agentHooks, snapConfig)
		assert.NoError(t, err)

		assert.NotNil(t, rr)

		// Wait a tiny bit for the VM to do things...
		time.Sleep(10 * time.Second)

		err = rr.SuspendAndCloseAgentServer(ctx, 10*time.Second)
		assert.NoError(t, err)

		previousRunner = r
	}

}

func calcHash(f string) []byte {
	hasher := sha256.New()

	buffer := make([]byte, 1024*1024)

	fp, err := os.Open(f)
	if err != nil {
		panic(err)
	}
	for {
		n, err := fp.Read(buffer)
		if err != nil && err != io.EOF {
			panic(err)
		}

		hasher.Write(buffer[:n])
		if err == io.EOF {
			break
		}
	}
	return hasher.Sum(nil)
}
