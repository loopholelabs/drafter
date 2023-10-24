package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	packagePath := flag.String("package-path", filepath.Join("out", "redis.ark"), "Path to package to use")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	loop := utils.NewLoop(*packagePath)

	packageDevicePath, err := loop.Open()
	if err != nil {
		panic(err)
	}
	defer loop.Close()

	packageConfigFile, err := os.Open(packageDevicePath)
	if err != nil {
		panic(err)
	}
	defer packageConfigFile.Close()

	packageConfig, err := utils.ReadPackageConfigFromEXT4Filesystem(packageConfigFile)
	if err != nil {
		panic(err)
	}
	_ = packageConfigFile.Close()

	runner := roles.NewRunner(
		utils.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NetNS:         *netns,
			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		utils.AgentConfiguration{
			AgentVSockPort: packageConfig.AgentVSockPort,
		},
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := runner.Wait(); err != nil {
			panic(err)
		}
	}()

	defer runner.Close()

	before := time.Now()

	if err := runner.Resume(ctx, packageDevicePath); err != nil {
		panic(err)
	}

	log.Println("Resume:", time.Since(before))

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	<-done

	before = time.Now()

	if err := runner.Suspend(ctx); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))
}
