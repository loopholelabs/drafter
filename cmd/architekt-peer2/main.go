package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/architekt/pkg/config"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	iservices "github.com/pojntfx/r3map/pkg/services"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type stage1 struct {
	name  string
	raddr string
	laddr string
}

type stage2 struct {
	prev stage1

	remote *iservices.SeederRemote
	size   int64
	cache  *os.File
}

func main() {
	// rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	// rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	// chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")
	cacheBaseDir := flag.String("cache-base-dir", filepath.Join("out", "cache"), "Cache base directory")

	// uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	// gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	// enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	// enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	// resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent to resume")

	// netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	// numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	// cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	stateRaddr := flag.String("state-raddr", "localhost:1500", "Remote address for state")
	memoryRaddr := flag.String("memory-raddr", "localhost:1501", "Remote address for memory")
	initramfsRaddr := flag.String("initramfs-raddr", "localhost:1502", "Remote address for initramfs")
	kernelRaddr := flag.String("kernel-raddr", "localhost:1503", "Remote address for kernel")
	diskRaddr := flag.String("disk-raddr", "localhost:1504", "Remote address for disk")

	stateLaddr := flag.String("state-laddr", ":1500", "Listen address for state")
	memoryLaddr := flag.String("memory-laddr", ":1501", "Listen address for memory")
	initramfsLaddr := flag.String("initramfs-laddr", ":1502", "Listen address for initramfs")
	kernelLaddr := flag.String("kernel-laddr", ":1503", "Listen address for kernel")
	diskLaddr := flag.String("disk-laddr", ":1504", "Listen address for disk")

	// pullWorkers := flag.Int64("pull-workers", 4096, "Pull workers to launch in the background; pass in a negative value to disable preemptive pull")

	// verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	// if err != nil {
	// 	panic(err)
	// }

	// jailerBin, err := exec.LookPath(*rawJailerBin)
	// if err != nil {
	// 	panic(err)
	// }

	stage1Inputs := []stage1{
		{
			name:  config.InitramfsName,
			raddr: *initramfsRaddr,
			laddr: *initramfsLaddr,
		},
		{
			name:  config.KernelName,
			raddr: *kernelRaddr,
			laddr: *kernelLaddr,
		},
		{
			name:  config.DiskName,
			raddr: *diskRaddr,
			laddr: *diskLaddr,
		},

		{
			name:  config.StateName,
			raddr: *stateRaddr,
			laddr: *stateLaddr,
		},
		{
			name:  config.MemoryName,
			raddr: *memoryRaddr,
			laddr: *memoryLaddr,
		},
	}

	var agentVSockPort uint32
	stage1Outputs, stage1Defers, stage1Errs := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input stage1, output *stage2, addDefer func(deferFunc func() error)) error {
			output.prev = input

			conn, err := grpc.Dial(input.raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			addDefer(conn.Close)

			remote, remoteWithMeta := services.NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))
			output.remote = remote

			size, port, err := remoteWithMeta.Meta(ctx)
			if err != nil {
				return err
			}
			output.size = size

			if index == 0 {
				agentVSockPort = port
			}

			if err := os.MkdirAll(*cacheBaseDir, os.ModePerm); err != nil {
				return err
			}

			cache, err := os.CreateTemp(*cacheBaseDir, "*.ark")
			if err != nil {
				return err
			}
			output.cache = cache
			addDefer(cache.Close)
			addDefer(func() error {
				return os.Remove(cache.Name())
			})

			if err := cache.Truncate(size); err != nil {
				return err
			}

			return nil
		},
	)

	for _, deferFuncs := range stage1Defers {
		for _, deferFunc := range deferFuncs {
			_ = deferFunc()
		}
	}

	for _, err := range stage1Errs {
		if err != nil {
			panic(err)
		}
	}

	log.Println(agentVSockPort, stage1Outputs)
}
