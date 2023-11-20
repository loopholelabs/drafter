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
	"reflect"
	"sync"
	"syscall"
	"time"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/architekt/pkg/config"
	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/mount"
	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	"github.com/pojntfx/r3map/pkg/migration"
	"golang.org/x/sys/unix"

	"github.com/loopholelabs/architekt/pkg/services"
	iservices "github.com/pojntfx/r3map/pkg/services"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type resource struct {
	raddr string
	path  string

	bar    *progressbar.ProgressBar
	local  backend.Backend
	mgr    *migration.PathMigrator
	size   int64
	remote *iservices.SeederRemote
	cache  *os.File

	finalize func() (seed func() (svc *iservices.SeederService, err error), path string, err error)
	finished chan struct{}

	err  error
	seed func() (svc *iservices.SeederService, err error)

	laddr string

	server *grpc.Server
	lis    net.Listener
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

	var agentVSockPort uint32
	resources := []*resource{
		{
			raddr: *stateRaddr,
			path:  firecracker.StateName,

			finished: make(chan struct{}),

			laddr: *stateLaddr,
		},
		{
			raddr: *memoryRaddr,
			path:  firecracker.MemoryName,

			finished: make(chan struct{}),

			laddr: *memoryLaddr,
		},
		{
			raddr: *initramfsRaddr,
			path:  roles.InitramfsName,

			finished: make(chan struct{}),

			laddr: *initramfsLaddr,
		},
		{
			raddr: *kernelRaddr,
			path:  roles.KernelName,

			finished: make(chan struct{}),

			laddr: *kernelLaddr,
		},
		{
			raddr: *diskRaddr,
			path:  roles.DiskName,

			finished: make(chan struct{}),

			laddr: *diskLaddr,
		},
	}
	for _, rsc := range resources {
		r := rsc

		conn, err := grpc.Dial(r.raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		remote, remoteWithMeta := services.NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))
		r.remote = remote

		size, port, err := remoteWithMeta.Meta(ctx)
		if err != nil {
			panic(err)
		}
		r.size = size

		agentVSockPort = port

		if err := os.MkdirAll(*cacheBaseDir, os.ModePerm); err != nil {
			panic(err)
		}

		cache, err := os.CreateTemp(*cacheBaseDir, "*.ark")
		if err != nil {
			panic(err)
		}
		r.cache = cache
		defer cache.Close()
		defer os.Remove(cache.Name())

		if err := cache.Truncate(size); err != nil {
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

	suspendStart := false
	suspendStartWg := sync.NewCond(&sync.Mutex{})

	var suspendErr error

	var suspendEndWg sync.WaitGroup
	suspendEndWg.Add(1)

	go func() {
		defer suspendEndWg.Done()

		suspendStartWg.L.Lock()
		if !suspendStart {
			suspendStartWg.Wait()
		}
		suspendStartWg.L.Unlock()

		before := time.Now()
		defer func() {
			log.Println("Suspend:", time.Since(before))
		}()

		log.Println("Suspending VM")

		if err := runner.Suspend(ctx); err != nil {
			suspendErr = err
		}
	}()

	stopStart := false
	stopStartWg := sync.NewCond(&sync.Mutex{})

	var stopErr error

	var stopEndWg sync.WaitGroup
	stopEndWg.Add(1)

	go func() {
		defer stopEndWg.Done()

		stopStartWg.L.Lock()
		if !stopStart {
			stopStartWg.Wait()
		}
		stopStartWg.L.Unlock()

		log.Println("Stopping VM")

		if err := runner.Close(); err != nil {
			stopErr = err
		}
	}()

	for i, rsc := range resources {
		r := rsc

		r.bar = progressbar.NewOptions64(
			r.size,
			progressbar.OptionSetDescription("Pulling "+r.path),
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

		r.bar.Add(client.MaximumBlockSize)

		r.local = backend.NewFileBackend(r.cache)
		r.mgr = migration.NewPathMigrator(
			ctx,

			r.local,

			&migration.MigratorOptions{
				Verbose:     *verbose,
				PullWorkers: *pullWorkers,
			},
			&migration.MigratorHooks{
				OnBeforeSync: func() error {
					suspendStartWg.L.Lock()
					suspendStart = true
					suspendStartWg.Broadcast()
					suspendStartWg.L.Unlock()

					suspendEndWg.Wait()

					return suspendErr
				},
				OnAfterSync: func(dirtyOffsets []int64) error {
					r.bar.Clear()

					delta := (len(dirtyOffsets) * client.MaximumBlockSize)

					log.Printf("Invalidated %v: %.2f MB (%.2f Mb)", r.path, float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

					r.bar.ChangeMax64(r.size + int64(delta))

					r.bar.Describe("Finalizing " + r.path)

					return nil
				},

				OnBeforeClose: func() error {
					stopStartWg.L.Lock()
					stopStart = true
					stopStartWg.Broadcast()
					stopStartWg.L.Unlock()

					stopEndWg.Wait()

					return stopErr
				},

				OnChunkIsLocal: func(off int64) error {
					r.bar.Add(client.MaximumBlockSize)

					return nil
				},
			},

			nil,
			nil,
		)

		go func(r *resource) {
			defer close(r.finished)

			if err := r.mgr.Wait(); err != nil {
				panic(err)
			}
		}(r)

		if i == 0 {
			done := make(chan os.Signal, 1)
			signal.Notify(done, os.Interrupt)
			go func() {
				<-done

				log.Println("Exiting gracefully")

				for _, rsc := range resources {
					r := rsc

					if r.mgr != nil {
						_ = r.mgr.Close()
					}
				}
			}()
		}

		log.Println("Leeching", r.path, "from", r.raddr)

		defer r.mgr.Close()
		r.finalize, _, err = r.mgr.Leech(r.remote)
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
	for _, r := range resources {
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

	var finalizeWg sync.WaitGroup
	for _, rsc := range resources {
		r := rsc

		finalizeWg.Add(1)

		go func(r *resource) {
			defer finalizeWg.Done()

			seed, file, err := r.finalize()
			if err != nil {
				r.err = err

				return
			}
			r.seed = seed

			info, err := os.Stat(file)
			if err != nil {
				r.err = err

				return
			}

			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				r.err = mount.ErrCouldNotGetDeviceStat

				return
			}

			major := uint64(stat.Rdev / 256)
			minor := uint64(stat.Rdev % 256)

			dev := int((major << 8) | minor)

			if err := unix.Mknod(filepath.Join(vmPath, firecracker.MountName, r.path), unix.S_IFBLK|0666, dev); err != nil {
				r.err = err

				return
			}

			r.bar.Clear()
		}(r)
	}
	finalizeWg.Wait()

	for _, r := range resources {
		if r.err != nil {
			panic(r.err)
		}
	}

	log.Println("Resuming VM on", vmPath)

	if err := runner.Resume(ctx); err != nil {
		panic(err)
	}

	defer runner.Close()

	log.Println("Resume:", time.Since(before))

	var seedWg sync.WaitGroup
	for _, rsc := range resources {
		r := rsc

		seedWg.Add(1)

		go func(r *resource) {
			defer seedWg.Done()

			svc, err := r.seed()
			if err != nil {
				r.err = err

				return
			}

			r.server = grpc.NewServer()

			v1.RegisterSeederWithMetaServer(r.server, services.NewSeederWithMetaServiceGrpc(services.NewSeederWithMetaService(svc, r.local, agentVSockPort, *verbose)))

			r.lis, err = net.Listen("tcp", r.laddr)
			if err != nil {
				r.err = err

				return
			}

			log.Println("Seeding", r.path, "on", r.laddr)
		}(r)
	}
	seedWg.Wait()

	defer func() {
		for _, rsc := range resources {
			r := rsc

			if r.lis != nil {
				_ = r.lis.Close()
			}
		}
	}()

	for _, r := range resources {
		if r.err != nil {
			panic(r.err)
		}
	}

	errs := make(chan error)
	for _, rsc := range resources {
		r := rsc

		go func(r *resource) {
			defer r.lis.Close()

			if err := r.server.Serve(r.lis); err != nil {
				if !utils.IsClosedErr(err) {
					errs <- err
				}

				return
			}
		}(r)
	}

	cases = []reflect.SelectCase{
		{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(errs),
		},
	}
	for _, r := range resources {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(r.finished),
		})
	}

	chosen, recv, _ := reflect.Select(cases)
	if chosen == 0 {
		// errs was selected, panic
		panic(recv)
	} else {
		// One of the finished channels was selected, return
		return
	}
}
