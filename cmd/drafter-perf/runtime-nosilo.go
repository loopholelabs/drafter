package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	loggingtypes "github.com/loopholelabs/logging/types"
)

/**
 * Run a benchmark with Silo disabled.
 *
 */
func runNonSilo(ctx context.Context, log loggingtypes.Logger, testDir string, snapDir string, ns func() (string, func(), error), forwards func(string) (func(), error), benchCB func(), enableInput bool, enableOutput bool) error {
	// NOW TRY WITHOUT SILO
	agentVsockPort := uint32(26)
	agentLocal := struct{}{}

	deviceFiles := []string{
		"state", "memory", "kernel", "disk", "config", "oci",
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	netns, nscloser, err := ns()
	if err != nil {
		return err
	}
	defer nscloser()

	fclose, err := forwards(netns)
	if err != nil {
		return err
	}
	defer fclose()

	conf := &rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  testDir,
		UID:            0,
		GID:            0,
		NetNS:          netns,
		NumaNode:       0,
		CgroupVersion:  2,
		Stdout:         nil,
		Stderr:         nil,
	}

	if enableInput {
		conf.Stdin = os.Stdin
	}

	if enableOutput {
		conf.Stdout = os.Stdout
		conf.Stderr = os.Stderr
	} else {
		fout, err := os.OpenFile("nosilo.stdout", os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0660)
		if err == nil {
			defer fout.Close()
			conf.Stdout = fout
			conf.Stderr = fout
		} else {
			fmt.Printf("Could not open output file? %v\n", err)
		}
	}

	m, err := rfirecracker.StartFirecrackerMachine(ctx, log, conf)
	if err != nil {
		return err
	}

	// Copy the devices in from the last place... (No Silo in the mix here)
	for _, d := range deviceFiles {
		src := path.Join(snapDir, d)
		dst := path.Join(m.VMPath, d)
		err = os.Link(src, dst)
		if err != nil {
			return err
		}
	}

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = m.ResumeSnapshot(resumeSnapshotAndAcceptCtx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		return err
	}

	agent, err := ipc.StartAgentRPC[struct{}, ipc.AgentServerRemote[struct{}]](
		log, path.Join(m.VMPath, rfirecracker.VSockName),
		agentVsockPort, agentLocal)
	if err != nil {
		return err
	}

	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelAfterResumeCtx()

	r, err := agent.GetRemote(afterResumeCtx)
	if err != nil {
		return err
	}

	remote := *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		return err
	}

	benchCB()

	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSuspendCtx()

	r, err = agent.GetRemote(suspendCtx)
	if err != nil {
		return err
	}

	remote = *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	if err != nil {
		return err
	}

	err = agent.Close()
	if err != nil {
		return err
	}

	return nil
}
