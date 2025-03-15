package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
)

const resumeTestDir = "resume_suspend_test"
const snapshotDir = "snap_test"

var blueprintsDir *string

func main() {
	defer func() {
		os.RemoveAll(snapshotDir)
		os.RemoveAll(resumeTestDir)
	}()

	iterations := flag.Int("num", 10, "number of iterations")
	sleepTime := flag.Duration("sleep", 5*time.Second, "sleep inbetween resume/suspend")
	blueprintsDir = flag.String("blueprints", "blueprints", "blueprints dir")

	flag.Parse()

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.DebugLevel)

	err := os.Mkdir(resumeTestDir, 0777)
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snapDir := setupSnapshot(log, ctx)

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
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

	// Resume and suspend the vm a few times
	for n := 0; n < *iterations; n++ {
		fmt.Printf("\nRUN %d\n", n)
		r, err := rfirecracker.StartRunner(log, ctx, ctx, rfirecracker.HypervisorConfiguration{
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
		if err != nil {
			panic(err)
		}

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
			if err != nil {
				panic(err)
			}
		}

		rr, err := rfirecracker.Resume[struct{}, ipc.AgentServerRemote[struct{}], struct{}](r, ctx, 30*time.Second, 30*time.Second,
			agentVsockPort, agentLocal, agentHooks, snapConfig)
		if err != nil {
			panic(err)
		}

		// Wait a tiny bit for the VM to do things...
		time.Sleep(*sleepTime)

		err = rr.SuspendAndCloseAgentServer(ctx, 10*time.Second)
		if err != nil {
			panic(err)
		}

		previousRunner = r
	}

	fmt.Printf("\nDONE\n")
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

/*
*
  - Pre-requisites
  - - ark0 network namespace exists

1.91 GB 2025-03-05T00:03:21Z
oci-ollama-x86_64.tar.zst
  - - firecracker works
  - - blueprints exist
*/
func setupSnapshot(log types.Logger, ctx context.Context) string {
	err := os.Mkdir(snapshotDir, 0777)
	if err != nil {
		panic(err)
	}

	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		panic(err)
	}

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
			Input:  path.Join(*blueprintsDir, "vmlinux"),
			Output: path.Join(snapshotDir, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(*blueprintsDir, "rootfs.ext4"),
			Output: path.Join(snapshotDir, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(snapshotDir, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(*blueprintsDir, "oci.ext4"),
			Output: path.Join(snapshotDir, "oci"),
		},
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, true,
		rfirecracker.VMConfiguration{
			CPUCount:    1,
			MemorySize:  1024,
			CPUTemplate: "T2A",
			BootArgs:    rfirecracker.DefaultBootArgs,
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

	if err != nil {
		panic(err)
	}

	return snapshotDir
}
