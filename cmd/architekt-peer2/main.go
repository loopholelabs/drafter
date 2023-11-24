package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/architekt/pkg/config"
	"github.com/loopholelabs/architekt/pkg/mount"
	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	"github.com/pojntfx/r3map/pkg/migration"
	iservices "github.com/pojntfx/r3map/pkg/services"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sys/unix"
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

type stage3 struct {
	prev stage2

	bar      *progressbar.ProgressBar
	local    backend.Backend
	mgr      *migration.PathMigrator
	finished chan struct{}
	finalize func() (seed func() (svc *iservices.SeederService, err error), err error)
}

type stage4 struct {
	prev stage3

	seed func() (svc *iservices.SeederService, err error)
}

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
	stage2Inputs, stage1Defers, stage1Errs := utils.ConcurrentMap(
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
			defer deferFunc()
		}
	}

	for _, err := range stage1Errs {
		if err != nil {
			panic(err)
		}
	}

	runner := roles.NewRunner(
		config.HypervisorConfiguration{
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
		config.AgentConfiguration{
			AgentVSockPort: agentVSockPort,
			ResumeTimeout:  *resumeTimeout,
		},

		config.StateName,
		config.MemoryName,
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
	vmPath, err := runner.Open()
	if err != nil {
		panic(err)
	}

	suspendVM := sync.OnceValue(func() error {
		before := time.Now()
		defer func() {
			log.Println("Suspend:", time.Since(before))
		}()

		log.Println("Suspending VM")

		return runner.Suspend(ctx)
	})

	stopVM := sync.OnceValue(func() error {
		log.Println("Stopping VM")

		return runner.Close()
	})

	var nbdDevicesLock sync.Mutex
	mgrs := []*migration.PathMigrator{}
	var mgrsLock sync.Mutex
	stage3Inputs, stage2Defers, stage2Errs := utils.ConcurrentMap(
		stage2Inputs,
		func(index int, input stage2, output *stage3, addDefer func(deferFunc func() error)) error {
			output.prev = input
			output.finished = make(chan struct{})

			output.bar = progressbar.NewOptions64(
				input.size,
				progressbar.OptionSetDescription("Pulling "+input.prev.name),
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

			output.bar.Add(client.MaximumBlockSize)

			output.local = backend.NewFileBackend(input.cache)
			output.mgr = migration.NewPathMigrator(
				ctx,

				output.local,

				&migration.MigratorOptions{
					Verbose:     *verbose,
					PullWorkers: *pullWorkers,
				},
				&migration.MigratorHooks{
					OnBeforeSync: func() error {
						return suspendVM()
					},
					OnAfterSync: func(dirtyOffsets []int64) error {
						output.bar.Clear()

						delta := (len(dirtyOffsets) * client.MaximumBlockSize)

						log.Printf("Invalidated %v: %.2f MB (%.2f Mb)", input.prev.name, float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

						output.bar.ChangeMax64(input.size + int64(delta))

						output.bar.Describe("Finalizing " + input.prev.name)

						return nil
					},

					OnBeforeClose: func() error {
						return stopVM()
					},

					OnChunkIsLocal: func(off int64) error {
						output.bar.Add(client.MaximumBlockSize)

						return nil
					},
				},

				nil,
				nil,
			)

			go func() {
				defer close(output.finished)

				if err := output.mgr.Wait(); err != nil {
					if !utils.IsClosedErr(err) {
						panic(err)
					}
				}
			}()

			mgrsLock.Lock()
			mgrs = append(mgrs, output.mgr)
			mgrsLock.Unlock()

			if index == 0 {
				done := make(chan os.Signal, 1)
				signal.Notify(done, os.Interrupt)
				go func() {
					<-done

					log.Println("Exiting gracefully")

					mgrsLock.Lock()
					defer mgrsLock.Unlock()

					for _, m := range mgrs {
						_ = m.Close()
					}
				}()
			}

			nbdDevicesLock.Lock() // We need to make sure that we call `FindUnusedNBDDevice` synchronously
			defer nbdDevicesLock.Unlock()

			addDefer(output.mgr.Close)
			finalize, file, _, err := output.mgr.Leech(output.prev.remote)
			if err != nil {
				return err
			}
			output.finalize = finalize

			info, err := os.Stat(file)
			if err != nil {
				return err
			}

			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return mount.ErrCouldNotGetDeviceStat
			}

			major := uint64(stat.Rdev / 256)
			minor := uint64(stat.Rdev % 256)

			dev := int((major << 8) | minor)

			if err := unix.Mknod(filepath.Join(vmPath, input.prev.name), unix.S_IFBLK|0666, dev); err != nil {
				return err
			}

			log.Println("Leeching", input.prev.name, "from", input.prev.raddr, "to", file)

			return nil
		},
	)

	for _, deferFuncs := range stage2Defers {
		for _, deferFunc := range deferFuncs {
			defer deferFunc()
		}
	}

	for _, err := range stage2Errs {
		if err != nil {
			panic(err)
		}
	}

	log.Println("Press <ENTER> to finalize migration")

	continueCh := make(chan struct{})
	go func() {
		bufio.NewScanner(os.Stdin).Scan()

		continueCh <- struct{}{}
	}()

	cases := []reflect.SelectCase{
		{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(continueCh),
		},
	}
	for _, r := range stage3Inputs {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(r.finished),
		})
	}

	chosen, _, _ := reflect.Select(cases)
	if chosen == 0 {
		// continueCh was selected, continue
	} else {
		// One of the finished channels was selected, return
		return
	}

	before := time.Now()

	stage4Inputs, stage3Defers, stage3Errs := utils.ConcurrentMap(
		stage3Inputs,
		func(index int, input stage3, output *stage4, addDefer func(deferFunc func() error)) error {
			output.prev = input

			seed, err := input.finalize()
			if err != nil {
				return err
			}
			output.seed = seed

			input.bar.Clear()

			return nil
		},
	)

	for _, deferFuncs := range stage3Defers {
		for _, deferFunc := range deferFuncs {
			defer deferFunc()
		}
	}

	for _, err := range stage3Errs {
		if err != nil {
			panic(err)
		}
	}

	log.Println("Resuming VM on", vmPath)

	defer runner.Close()
	if err := runner.Resume(ctx); err != nil {
		panic(err)
	}

	log.Println("Resume:", time.Since(before))

	log.Println(stage4Inputs)
}
