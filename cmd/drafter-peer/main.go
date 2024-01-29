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

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/r3map/pkg/migration"
	"github.com/vbauerster/mpb/v8"
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

	stateRaddr := flag.String("state-raddr", "localhost:1500", "Remote address for state (leave empty to use path)")
	memoryRaddr := flag.String("memory-raddr", "localhost:1501", "Remote address for memory (leave empty to use path)")
	initramfsRaddr := flag.String("initramfs-raddr", "localhost:1502", "Remote address for initramfs (leave empty to use path)")
	kernelRaddr := flag.String("kernel-raddr", "localhost:1503", "Remote address for kernel (leave empty to use path)")
	diskRaddr := flag.String("disk-raddr", "localhost:1504", "Remote address for disk (leave empty to use path)")
	configRaddr := flag.String("config-raddr", "localhost:1505", "Remote address for config (leave empty to use path)")

	statePath := flag.String("state-path", "", "Path for state")
	memoryPath := flag.String("memory-path", "", "Path for memory")
	initramfsPath := flag.String("initramfs-path", "", "Path for initramfs")
	kernelPath := flag.String("kernel-path", "", "Path for kernel")
	diskPath := flag.String("disk-path", "", "Path for disk")
	configPath := flag.String("config-path", "", "Path for config")

	stateLaddr := flag.String("state-laddr", ":1500", "Listen address for state")
	memoryLaddr := flag.String("memory-laddr", ":1501", "Listen address for memory")
	initramfsLaddr := flag.String("initramfs-laddr", ":1502", "Listen address for initramfs")
	kernelLaddr := flag.String("kernel-laddr", ":1503", "Listen address for kernel")
	diskLaddr := flag.String("disk-laddr", ":1504", "Listen address for disk")
	configLaddr := flag.String("config-laddr", ":1505", "Listen address for config")

	stateResumeThreshold := flag.Int64("state-resume-threshold", 0, "Amount of remaining state data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the state is 100% locally available)")
	memoryResumeThreshold := flag.Int64("memory-resume-threshold", 0, "Amount of remaining memory data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the memory is 100% locally available)")
	initramfsResumeThreshold := flag.Int64("initramfs-resume-threshold", 0, "Amount of remaining initramfs data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the initramfs is 100% locally available)")
	kernelResumeThreshold := flag.Int64("kernel-resume-threshold", 0, "Amount of remaining kernel data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the kernel is 100% locally available)")
	diskResumeThreshold := flag.Int64("disk-resume-threshold", 0, "Amount of remaining disk data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the disk is 100% locally available)")
	configResumeThreshold := flag.Int64("config-resume-threshold", 0, "Amount of remaining config data after which to start resuming the VM (in B; 0 means continuing to resume the VM after the config is 100% locally available)")

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

	p := mpb.New()
	var (
		stateBar     = utils.NewProgressBar(p, "Pulling state")
		memoryBar    = utils.NewProgressBar(p, "Pulling memory")
		initramfsBar = utils.NewProgressBar(p, "Pulling initramfs")
		kernelBar    = utils.NewProgressBar(p, "Pulling kernel")
		diskBar      = utils.NewProgressBar(p, "Pulling disk")
		configBar    = utils.NewProgressBar(p, "Pulling config")
	)

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
				stateBar.Clear()
				memoryBar.Clear()
				initramfsBar.Clear()
				kernelBar.Clear()
				diskBar.Clear()
				configBar.Clear()

				p.Wait()

				log.Println("Suspending VM")

				beforeSuspend = time.Now()

				return nil
			},
			OnAfterSuspend: func() error {
				stateBar.Clear()
				memoryBar.Clear()
				initramfsBar.Clear()
				kernelBar.Clear()
				diskBar.Clear()
				configBar.Clear()

				p.Wait()

				log.Println("Suspended VM in", time.Since(beforeSuspend))

				return nil
			},

			OnBeforeStop: func() error {
				stateBar.Clear()
				memoryBar.Clear()
				initramfsBar.Clear()
				kernelBar.Clear()
				diskBar.Clear()
				configBar.Clear()

				p.Wait()

				log.Println("Stopping VM")

				beforeStop = time.Now()

				return nil
			},
			OnAfterStop: func() error {
				stateBar.Clear()
				memoryBar.Clear()
				initramfsBar.Clear()
				kernelBar.Clear()
				diskBar.Clear()
				configBar.Clear()

				p.Wait()

				log.Println("Stopped VM in", time.Since(beforeStop))

				return nil
			},

			OnStateLeechProgress: func(remainingDataSize int64) error {
				stateBar.SetRemaining(remainingDataSize)

				return nil
			},
			OnMemoryLeechProgress: func(remainingDataSize int64) error {
				memoryBar.SetRemaining(remainingDataSize)

				return nil
			},
			OnInitramfsLeechProgress: func(remainingDataSize int64) error {
				initramfsBar.SetRemaining(remainingDataSize)

				return nil
			},
			OnKernelLeechProgress: func(remainingDataSize int64) error {
				kernelBar.SetRemaining(remainingDataSize)

				return nil
			},
			OnDiskLeechProgress: func(remainingDataSize int64) error {
				diskBar.SetRemaining(remainingDataSize)

				return nil
			},
			OnConfigLeechProgress: func(remainingDataSize int64) error {
				configBar.SetRemaining(remainingDataSize)

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
			Config:    *configRaddr,
		},
		config.ResourceAddresses{
			State:     *statePath,
			Memory:    *memoryPath,
			Initramfs: *initramfsPath,
			Kernel:    *kernelPath,
			Disk:      *diskPath,
			Config:    *configPath,
		},
		config.ResourceAddresses{
			State:     *stateLaddr,
			Memory:    *memoryLaddr,
			Initramfs: *initramfsLaddr,
			Kernel:    *kernelLaddr,
			Disk:      *diskLaddr,
			Config:    *configLaddr,
		},
		config.ResourceResumeThresholds{
			State:     *stateResumeThreshold,
			Memory:    *memoryResumeThreshold,
			Initramfs: *initramfsResumeThreshold,
			Kernel:    *kernelResumeThreshold,
			Disk:      *diskResumeThreshold,
			Config:    *configResumeThreshold,
		},

		*resumeTimeout,
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

		stateBar.Clear()
		memoryBar.Clear()
		initramfsBar.Clear()
		kernelBar.Clear()
		diskBar.Clear()
		configBar.Clear()

		p.Wait()

		log.Println("Exiting gracefully")

		_ = peer.Close(true)
	}()

	defer peer.Close(false)
	sizes, err := peer.Connect(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		defer stateBar.Pause()()
		defer memoryBar.Pause()()
		defer initramfsBar.Pause()()
		defer kernelBar.Pause()()
		defer diskBar.Pause()()
		defer configBar.Pause()()

		log.Printf("Leeching state (%v bytes)", sizes.State)
		log.Printf("Leeching memory (%v bytes)", sizes.Memory)
		log.Printf("Leeching initramfs (%v bytes)", sizes.Initramfs)
		log.Printf("Leeching kernel (%v bytes)", sizes.Kernel)
		log.Printf("Leeching disk (%v bytes)", sizes.Disk)
		log.Printf("Leeching config (%v bytes)", sizes.Config)
	}()

	func() {
		stateBar.SetTotal(sizes.State)
		memoryBar.SetTotal(sizes.Memory)
		initramfsBar.SetTotal(sizes.Initramfs)
		kernelBar.SetTotal(sizes.Kernel)
		diskBar.SetTotal(sizes.Disk)
		configBar.SetTotal(sizes.Config)
	}()

	deltas, err := peer.Leech(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		defer stateBar.Pause()()
		defer memoryBar.Pause()()
		defer initramfsBar.Pause()()
		defer kernelBar.Pause()()
		defer diskBar.Pause()()
		defer configBar.Pause()()

		log.Printf("Leeched state (%v bytes invalidated)", deltas.State)
		log.Printf("Leeched memory (%v bytes invalidated)", deltas.Memory)
		log.Printf("Leeched initramfs (%v bytes invalidated)", deltas.Initramfs)
		log.Printf("Leeched kernel (%v bytes invalidated)", deltas.Kernel)
		log.Printf("Leeched config (%v bytes invalidated)", deltas.Config)
	}()

	func() {
		stateBar.IncreaseTotal(int64(deltas.State))
		memoryBar.IncreaseTotal(int64(deltas.Memory))
		initramfsBar.IncreaseTotal(int64(deltas.Initramfs))
		kernelBar.IncreaseTotal(int64(deltas.Kernel))
		memoryBar.IncreaseTotal(int64(deltas.Disk))
		configBar.IncreaseTotal(int64(deltas.Config))
	}()

	func() {
		defer stateBar.Pause()()
		defer memoryBar.Pause()()
		defer initramfsBar.Pause()()
		defer kernelBar.Pause()()
		defer diskBar.Pause()()
		defer configBar.Pause()()

		log.Println("Resuming VM")
	}()

	before := time.Now()

	vmPath, err := peer.Resume(ctx)
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	func() {
		defer stateBar.Pause()()
		defer memoryBar.Pause()()
		defer initramfsBar.Pause()()
		defer kernelBar.Pause()()
		defer diskBar.Pause()()
		defer configBar.Pause()()

		log.Println("Resumed VM in", time.Since(before), "on", vmPath)
	}()

	laddrs, err := peer.Seed()
	if err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}

	log.Printf("\n\n\n\n\n\n") // Offset the progress bars to prevent accidentally removing regular outputs

	stateBar.Clear()
	memoryBar.Clear()
	initramfsBar.Clear()
	kernelBar.Clear()
	diskBar.Clear()
	configBar.Clear()

	p.Wait()

	log.Println("Seeding state on", laddrs.State)
	log.Println("Seeding memory on", laddrs.Memory)
	log.Println("Seeding initramfs on", laddrs.Initramfs)
	log.Println("Seeding kernel on", laddrs.Kernel)
	log.Println("Seeding disk on", laddrs.Disk)
	log.Println("Seeding config on", laddrs.Config)

	if err := peer.Serve(); err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}
}
