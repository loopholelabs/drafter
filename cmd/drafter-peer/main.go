package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/r3map/pkg/migration"
	"github.com/schollz/progressbar/v3"
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
	resumeThreshold := flag.Int64("resume-threshold", 0, "Amount of remaining data after which to start resuming the VM (in B; 0 means resuming the VM after it is 100% locally available)")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

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

	var barLock sync.Mutex
	bar := progressbar.NewOptions64(
		0,
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
	bar.Clear()

	var (
		beforeResume  = time.Now()
		beforeSuspend = time.Now()
		beforeStop    = time.Now()
	)

	peer := roles.NewPeer(
		*verbose,

		func() (string, error) {
			return *netns, nil
		},
		func(namespace string) error {
			return nil
		},

		config.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		&migration.MigratorOptions{
			Verbose:     *verbose,
			PullWorkers: *pullWorkers,
		},
		roles.PeerHooks{
			OnBeforeResume: func(vmPath string) error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Resuming VM on", vmPath)

				beforeResume = time.Now()

				return nil
			},
			OnAfterResume: func() error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Resumed VM in", time.Since(beforeResume))

				return nil
			},

			OnBeforeSuspend: func() error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Suspending VM")

				beforeSuspend = time.Now()

				return nil
			},
			OnAfterSuspend: func() error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Suspended VM in", time.Since(beforeSuspend))

				return nil
			},

			OnBeforeStop: func() error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Stopping VM")

				beforeStop = time.Now()

				return nil
			},
			OnAfterStop: func() error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Stopped VM in", time.Since(beforeStop))

				return nil
			},

			OnBeforeLeeching: func(name, raddr, file string, size int64) error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Leeching", name, "from", raddr, "to")

				bar.ChangeMax64(bar.GetMax64() + size)

				return nil
			},
			OnLeechProgress: func(remainingDataSize int64) error {
				barLock.Lock()
				defer barLock.Unlock()

				if diff := bar.GetMax64() - remainingDataSize; diff > 0 {
					bar.Set64(diff)
				}

				return nil
			},
			OnAfterSync: func(name string, delta int) error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Printf("Invalidated %v: %.2f MB (%.2f Mb)", name, float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

				bar.ChangeMax64(bar.GetMax64() + int64(delta))

				bar.Describe("Finalizing")

				return nil
			},
			OnAfterSeeding: func(name, laddr string) error {
				barLock.Lock()
				defer barLock.Unlock()

				bar.Clear()

				log.Println("Seeding", name, "on", laddr)

				return nil
			},
		},

		*cacheBaseDir,

		config.ResourceAddresses{
			State:     *stateRaddr,
			Memory:    *memoryRaddr,
			Initramfs: *initramfsRaddr,
			Kernel:    *kernelRaddr,
			Disk:      *diskRaddr,
		},
		config.ResourceAddresses{
			State:     *stateLaddr,
			Memory:    *memoryLaddr,
			Initramfs: *initramfsLaddr,
			Kernel:    *kernelLaddr,
			Disk:      *diskLaddr,
		},

		*resumeTimeout,
		*resumeThreshold,
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := peer.Wait(); err != nil {
			if !utils.IsClosedErr(err) {
				panic(err)
			}

			return
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done

		log.Println("Exiting gracefully")

		_ = peer.Close()
	}()

	defer peer.Close()
	if err := peer.Leech(ctx); err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	if err := peer.Seed(); err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}
}
