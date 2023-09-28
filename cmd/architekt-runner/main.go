package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", filepath.Join("/usr", "local", "bin", "firecracker"), "Firecracker binary")
	jailerBin := flag.String("jailer-bin", filepath.Join("/usr", "local", "bin", "jailer"), "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 123, "User ID for the Firecracker process")
	gid := flag.Int("gid", 100, "Group ID for the Firecracker process")

	netns := flag.String("netns", "ns1", "Network namespace to run Firecracke in")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	agentVSockPort := flag.Uint("agent-vsock-port", 26, "Agent VSock port")

	packagePath := flag.String("package-path", filepath.Join("out", "redis.ark"), "Path to package to use")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loop := utils.NewLoop(*packagePath)

	packageDevicePath, err := loop.Open()
	if err != nil {
		panic(err)
	}
	defer loop.Close()

	runner := roles.NewRunner(
		roles.HypervisorConfiguration{
			FirecrackerBin: *firecrackerBin,
			JailerBin:      *jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NetNS: *netns,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		roles.NetworkConfiguration{
			HostInterface:   *hostInterface,
			HostMAC:         *hostMAC,
			BridgeInterface: *bridgeInterface,
		},
		roles.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
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
	if err := runner.Open(); err != nil {
		panic(err)
	}

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
