package firecracker

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/loopholelabs/drafter/pkg/common"

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

	stateName := common.DeviceStateName
	memoryName := common.DeviceMemoryName

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
	}, stateName, memoryName)
	assert.NoError(t, err)

	err = r.Close()
	assert.NoError(t, err)
}
