//go:build integration
// +build integration

package firecracker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/testutil"

	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/stretchr/testify/assert"
)

const resumeTestDir = "resume_suspend_test"

/**
 * This creates a snapshot, and then resume and suspends 10 times.
 *
 * firecracker needs to work
 * blueprints expected to exist at ./out/blueprint
 *
 */
func TestResumeSuspend(t *testing.T) {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.ErrorLevel)

	err := os.Mkdir(resumeTestDir, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(resumeTestDir)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := testutil.SetupNAT(t, "", "dra")

	netns, err := ns.ClaimNamespace()
	assert.NoError(t, err)

	snapDir := setupSnapshot(t, log, ctx, netns, VMConfiguration{
		CPUCount:    1,
		MemorySize:  1024,
		CPUTemplate: "None",
		BootArgs:    DefaultBootArgsNoPVM,
	},
	)

	agentVsockPort := uint32(26)
	agentLocal := struct{}{}

	deviceFiles := []string{
		"state", "memory", "kernel", "disk", "config", "oci",
	}

	devicesAt := snapDir
	var previousMachine *FirecrackerMachine

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	// Resume and suspend the vm a few times
	for n := 0; n < 3; n++ {
		m, err := StartFirecrackerMachine(ctx, log, &FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  resumeTestDir,
			UID:            0,
			GID:            0,
			NetNS:          netns,
			NumaNode:       0,
			CgroupVersion:  2,
			EnableOutput:   false,
			EnableInput:    false,
		})
		assert.NoError(t, err)

		// Copy the devices in from the last place...
		for _, d := range deviceFiles {
			src := path.Join(devicesAt, d)
			dst := path.Join(m.VMPath, d)
			hash := calcHash(src)
			os.Link(src, dst)
			fmt.Printf("Device hash %x (%s)\n", hash, d)
		}
		devicesAt = m.VMPath

		// Now we can close the previous runner
		if previousMachine != nil {
			err = previousMachine.Close()
			assert.NoError(t, err)
			err = os.RemoveAll(filepath.Dir(previousMachine.VMPath))
			assert.NoError(t, err)
		}

		rr, err := Resume[struct{}, ipc.AgentServerRemote[struct{}], struct{}](m, ctx, 5*time.Second, 5*time.Second,
			agentVsockPort, agentLocal)
		assert.NoError(t, err)

		assert.NotNil(t, rr)

		// Wait a tiny bit for the VM to do things...
		time.Sleep(10 * time.Second)

		err = rr.SuspendAndCloseAgentServer(ctx, 10*time.Second)
		assert.NoError(t, err)

		previousMachine = m
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
