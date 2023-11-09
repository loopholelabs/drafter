package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	"github.com/pojntfx/r3map/pkg/migration"

	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")
	cacheBaseDir := flag.String("cache-base-dir", filepath.Join("out", "cache"), "Cache base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	raddr := flag.String("raddr", "localhost:1338", "Remote address")
	laddr := flag.String("laddr", ":1338", "Listen address")

	pullWorkers := flag.Int64("pull-workers", 4096, "Pull workers to launch in the background; pass in a negative value to disable preemptive pull")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

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

	conn, err := grpc.Dial(*raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	remote, remoteWithMeta := services.NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))

	size, agentVSockPort, err := remoteWithMeta.Meta(ctx)
	if err != nil {
		panic(err)
	}

	if err := os.MkdirAll(*cacheBaseDir, os.ModePerm); err != nil {
		panic(err)
	}

	cache, err := os.CreateTemp(*cacheBaseDir, "*.ark")
	if err != nil {
		panic(err)
	}
	defer cache.Close()
	defer os.Remove(cache.Name())

	if err := cache.Truncate(size); err != nil {
		panic(err)
	}

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
			AgentVSockPort: agentVSockPort,
			ResumeTimeout:  *resumeTimeout,
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

	bar := progressbar.NewOptions64(
		size,
		progressbar.OptionSetDescription("Pulling"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionFullWidth(),
		// VT-100 compatibility
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	bar.Add(client.MaximumBlockSize)

	b := backend.NewFileBackend(cache)
	mgr := migration.NewPathMigrator(
		ctx,

		b,

		&migration.MigratorOptions{
			Verbose:     *verbose,
			PullWorkers: *pullWorkers,
		},
		&migration.MigratorHooks{
			OnBeforeSync: func() error {
				before := time.Now()
				defer func() {
					log.Println("Suspend:", time.Since(before))
				}()

				log.Println("Suspending VM")

				return runner.Suspend(ctx)
			},
			OnAfterSync: func(dirtyOffsets []int64) error {
				bar.Clear()

				delta := (len(dirtyOffsets) * client.MaximumBlockSize)

				log.Printf("Invalidated: %.2f MB (%.2f Mb)", float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

				bar.ChangeMax64(size + int64(delta))

				bar.Describe("Finalizing")

				return nil
			},

			OnBeforeClose: func() error {
				log.Println("Stopping VM")

				return runner.Close()
			},

			OnChunkIsLocal: func(off int64) error {
				bar.Add(client.MaximumBlockSize)

				return nil
			},
		},

		nil,
		nil,
	)

	finished := make(chan struct{})
	go func() {
		defer close(finished)

		if err := mgr.Wait(); err != nil {
			panic(err)
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done

		log.Println("Exiting gracefully")

		_ = mgr.Close()
	}()

	log.Println("Leeching from", *raddr)

	defer mgr.Close()
	finalize, _, err := mgr.Leech(remote)
	if err != nil {
		panic(err)
	}

	log.Println("Press <ENTER> to finalize migration")

	continueCh := make(chan struct{})
	go func() {
		bufio.NewScanner(os.Stdin).Scan()

		continueCh <- struct{}{}
	}()

	select {
	case <-continueCh:
	case <-finished:
		return
	}

	before := time.Now()

	seed, file, err := finalize()
	if err != nil {
		panic(err)
	}

	bar.Clear()

	log.Println("Resuming VM on", file)

	if err := runner.Resume(ctx, file); err != nil {
		panic(err)
	}

	defer runner.Close()

	log.Println("Resume:", time.Since(before))

	svc, err := seed()
	if err != nil {
		panic(err)
	}

	server := grpc.NewServer()

	v1.RegisterSeederWithMetaServer(server, services.NewSeederWithMetaServiceGrpc(services.NewSeederWithMetaService(svc, b, agentVSockPort, *verbose)))

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Seeding on", *laddr)

	go func() {
		if err := server.Serve(lis); err != nil {
			if !utils.IsClosedErr(err) {
				panic(err)
			}

			return
		}
	}()

	<-finished
}
