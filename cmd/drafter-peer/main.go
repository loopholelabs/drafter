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

			OnLeechProgress: func(remainingDataSize int64) error {
				barLock.Lock()
				defer barLock.Unlock()

				if diff := bar.GetMax64() - remainingDataSize; diff > 0 {
					bar.Set64(diff)
				}

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
	sizes, err := peer.Connect(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		barLock.Lock()
		defer barLock.Unlock()

		bar.Clear()

		log.Printf("Leeching state (%v bytes)", sizes.State)
		log.Printf("Leeching memory (%v bytes)", sizes.Memory)
		log.Printf("Leeching initramfs (%v bytes)", sizes.Initramfs)
		log.Printf("Leeching kernel (%v bytes)", sizes.Kernel)
		log.Printf("Leeching disk (%v bytes)", sizes.Disk)

		bar.ChangeMax64(bar.GetMax64() + sizes.State + sizes.Memory + sizes.Initramfs + sizes.Kernel + sizes.Disk)
	}()

	deltas, err := peer.Leech(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		barLock.Lock()
		defer barLock.Unlock()

		bar.Clear()

		log.Printf("Leeched state (%v bytes invalidated)", deltas.State)
		log.Printf("Leeched memory (%v bytes invalidated)", deltas.Memory)
		log.Printf("Leeched initramfs (%v bytes invalidated)", deltas.Initramfs)
		log.Printf("Leeched kernel (%v bytes invalidated)", deltas.Kernel)
		log.Printf("Leeched disk (%v bytes invalidated)", deltas.Disk)

		bar.ChangeMax(bar.GetMax() + deltas.State + deltas.Memory + deltas.Initramfs + deltas.Kernel + deltas.Disk)
	}()

	log.Println("Resuming VM")

	before := time.Now()

	vmPath, err := peer.Resume(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	log.Println("Resumed VM in", time.Since(before), "on", vmPath)

	laddrs, err := peer.Seed()
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		barLock.Lock()
		defer barLock.Unlock()

		bar.Clear()

		log.Println("Seeding state on", laddrs.State)
		log.Println("Seeding memory on", laddrs.Memory)
		log.Println("Seeding initramfs on", laddrs.Initramfs)
		log.Println("Seeding kernel on", laddrs.Kernel)
		log.Println("Seeding disk on", laddrs.Disk)
	}()

	if err := peer.Serve(); err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}
}
