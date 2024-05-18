package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	stateBasePath := flag.String("state-base-path", filepath.Join("out", "package", "drafter.drftstate"), "State base path")
	memoryBasePath := flag.String("memory-base-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory base path")
	initramfsBasePath := flag.String("initramfs-base-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs base path")
	kernelBasePath := flag.String("kernel-base-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel base path")
	diskBasePath := flag.String("disk-base-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk base path")
	configBasePath := flag.String("config-base-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config base path")

	stateOverlayPath := flag.String("state-overlay-path", filepath.Join("out", "overlay", "drafter.drftstate.overlay"), "State overlay path (ignored for devices migrated from raddr)")
	memoryOverlayPath := flag.String("memory-overlay-path", filepath.Join("out", "overlay", "drafter.drftmemory.overlay"), "Memory overlay path (ignored for devices migrated from raddr)")
	initramfsOverlayPath := flag.String("initramfs-overlay-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.overlay"), "initramfs overlay path (ignored for devices migrated from raddr)")
	kernelOverlayPath := flag.String("kernel-overlay-path", filepath.Join("out", "overlay", "drafter.drftkernel.overlay"), "Kernel overlay path (ignored for devices migrated from raddr)")
	diskOverlayPath := flag.String("disk-overlay-path", filepath.Join("out", "overlay", "drafter.drftdisk.overlay"), "Disk overlay path (ignored for devices migrated from raddr)")
	configOverlayPath := flag.String("config-overlay-path", filepath.Join("out", "overlay", "drafter.drftroles.overlay"), "Config overlay path (ignored for devices migrated from raddr)")

	stateStatePath := flag.String("state-state-path", filepath.Join("out", "overlay", "drafter.drftstate.state"), "State state path (ignored for devices migrated from raddr)")
	memoryStatePath := flag.String("memory-state-path", filepath.Join("out", "overlay", "drafter.drftmemory.state"), "Memory state path (ignored for devices migrated from raddr)")
	initramfsStatePath := flag.String("initramfs-state-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.state"), "initramfs state path (ignored for devices migrated from raddr)")
	kernelStatePath := flag.String("kernel-state-path", filepath.Join("out", "overlay", "drafter.drftkernel.state"), "Kernel state path (ignored for devices migrated from raddr)")
	diskStatePath := flag.String("disk-state-path", filepath.Join("out", "overlay", "drafter.drftdisk.state"), "Disk state path (ignored for devices migrated from raddr)")
	configStatePath := flag.String("config-state-path", filepath.Join("out", "overlay", "drafter.drftroles.state"), "Config state path (ignored for devices migrated from raddr)")

	stateBlockSize := flag.Uint("state-block-size", 1024*64, "State block size")
	memoryBlockSize := flag.Uint("memory-block-size", 1024*64, "Memory block size")
	initramfsBlockSize := flag.Uint("initramfs-block-size", 1024*64, "initramfs block size")
	kernelBlockSize := flag.Uint("kernel-block-size", 1024*64, "Kernel block size")
	diskBlockSize := flag.Uint("disk-block-size", 1024*64, "Disk block size")
	configBlockSize := flag.Uint("config-block-size", 1024*64, "Config block size")

	stateMaxDirtyBlocks := flag.Int("state-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for state")
	memoryMaxDirtyBlocks := flag.Int("memory-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for memory")
	initramfsMaxDirtyBlocks := flag.Int("initramfs-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for initramfs")
	kernelMaxDirtyBlocks := flag.Int("kernel-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for kernel")
	diskMaxDirtyBlocks := flag.Int("disk-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for disk")
	configMaxDirtyBlocks := flag.Int("config-max-dirty-blocks", 200, "Maximum amount of dirty blocks per continous migration cycle after which to transfer authority for config")

	stateMinCycles := flag.Int("state-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for state")
	memoryMinCycles := flag.Int("memory-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for memory")
	initramfsMinCycles := flag.Int("initramfs-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for initramfs")
	kernelMinCycles := flag.Int("kernel-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for kernel")
	diskMinCycles := flag.Int("disk-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for disk")
	configMinCycles := flag.Int("config-min-cycles", 5, "Minimum amount of subsequent continuous migration cycles below the maximum dirty block count after which to transfer authority for config")

	stateMaxCycles := flag.Int("state-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for state, even if a cycle is above the maximum dirty block count")
	memoryMaxCycles := flag.Int("memory-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for memory, even if a cycle is above the maximum dirty block count")
	initramfsMaxCycles := flag.Int("initramfs-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for initramfs, even if a cycle is above the maximum dirty block count")
	kernelMaxCycles := flag.Int("kernel-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for kernel, even if a cycle is above the maximum dirty block count")
	diskMaxCycles := flag.Int("disk-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for disk, even if a cycle is above the maximum dirty block count")
	configMaxCycles := flag.Int("config-max-cycles", 20, "Maximum amount of total migration cycles after which to transfer authority for config, even if a cycle is above the maximum dirty block count")

	stateCycleThrottle := flag.Duration("state-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for state")
	memoryCycleThrottle := flag.Duration("memory-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for memory")
	initramfsCycleThrottle := flag.Duration("initramfs-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for initramfs")
	kernelCycleThrottle := flag.Duration("kernel-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for kernel")
	diskCycleThrottle := flag.Duration("disk-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for disk")
	configCycleThrottle := flag.Duration("config-cycle-throttle", time.Millisecond*500, "Time to wait in each cycle before checking for changes again during continuous migration for config")

	stateExpiry := flag.Duration("state-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for state")
	memoryExpiry := flag.Duration("memory-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for memory")
	initramfsExpiry := flag.Duration("initramfs-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for initramfs")
	kernelExpiry := flag.Duration("kernel-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for kernel")
	diskExpiry := flag.Duration("disk-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for disk")
	configExpiry := flag.Duration("config-expiry", time.Second, "Time without changes after which to expire a block's volatile status during continuous and final migration for config")

	stateServe := flag.Bool("state-serve", true, "Whether to serve the state")
	memoryServe := flag.Bool("memory-serve", true, "Whether to serve the memory")
	initramfsServe := flag.Bool("initramfs-serve", true, "Whether to serve the initramfs")
	kernelServe := flag.Bool("kernel-serve", true, "Whether to serve the kernel")
	diskServe := flag.Bool("disk-serve", true, "Whether to serve the disk")
	configServe := flag.Bool("config-serve", true, "Whether to serve the config")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")
	laddr := flag.String("laddr", "localhost:1337", "Local address to listen on (leave empty to disable)")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

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

	var (
		readers []io.Reader
		writers []io.Writer
	)
	if strings.TrimSpace(*raddr) != "" {
		conn, err := net.Dial("tcp", *raddr)
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		log.Println("Migrating from", conn.RemoteAddr())

		readers = []io.Reader{conn}
		writers = []io.Writer{conn}
	}

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	bubbleSignals := false

	done := make(chan os.Signal, 1)
	go func() {
		signal.Notify(done, os.Interrupt)

		v := <-done

		if bubbleSignals {
			done <- v

			return
		}

		log.Println("Exiting gracefully")

		cancel()
	}()

	peer, err := roles.StartPeer(
		ctx,
		context.Background(), // Never give up on rescue operations

		roles.HypervisorConfiguration{
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

		roles.StateName,
		roles.MemoryName,
	)

	if peer.Wait != nil {
		defer func() {
			defer handlePanics(true)()

			if err := peer.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := peer.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := peer.Wait(); err != nil {
			panic(err)
		}
	})

	migratedPeer, err := peer.MigrateFrom(
		ctx,

		roles.MigrateFromDeviceConfiguration{
			BasePath:    *stateBasePath,
			OverlayPath: *stateOverlayPath,
			StatePath:   *stateStatePath,

			BlockSize: uint32(*stateBlockSize),
		},
		roles.MigrateFromDeviceConfiguration{
			BasePath:    *memoryBasePath,
			OverlayPath: *memoryOverlayPath,
			StatePath:   *memoryStatePath,

			BlockSize: uint32(*memoryBlockSize),
		},
		roles.MigrateFromDeviceConfiguration{
			BasePath:    *initramfsBasePath,
			OverlayPath: *initramfsOverlayPath,
			StatePath:   *initramfsStatePath,

			BlockSize: uint32(*initramfsBlockSize),
		},
		roles.MigrateFromDeviceConfiguration{
			BasePath:    *kernelBasePath,
			OverlayPath: *kernelOverlayPath,
			StatePath:   *kernelStatePath,

			BlockSize: uint32(*kernelBlockSize),
		},
		roles.MigrateFromDeviceConfiguration{
			BasePath:    *diskBasePath,
			OverlayPath: *diskOverlayPath,
			StatePath:   *diskStatePath,

			BlockSize: uint32(*diskBlockSize),
		},
		roles.MigrateFromDeviceConfiguration{
			BasePath:    *configBasePath,
			OverlayPath: *configOverlayPath,
			StatePath:   *configStatePath,

			BlockSize: uint32(*configBlockSize),
		},

		readers,
		writers,

		roles.MigrateFromHooks{
			OnRemoteDeviceReceived: func(remoteDeviceID uint32, name string) {
				log.Println("Received remote device", remoteDeviceID, "with name", name)
			},
			OnRemoteDeviceExposed: func(remoteDeviceID uint32, path string) {
				log.Println("Exposed remote device", remoteDeviceID, "at", path)
			},
			OnRemoteDeviceAuthorityReceived: func(remoteDeviceID uint32) {
				log.Println("Received authority for remote device", remoteDeviceID)
			},
			OnRemoteDeviceMigrationCompleted: func(remoteDeviceID uint32) {
				log.Println("Completed migration of remote device", remoteDeviceID)
			},

			OnRemoteAllDevicesReceived: func() {
				log.Println("Received all remote devices")
			},
			OnRemoteAllMigrationsCompleted: func() {
				log.Println("Completed all remote device migrations")
			},

			OnLocalDeviceRequested: func(localDeviceID uint32, name string) {
				log.Println("Requested local device", localDeviceID, "with name", name)
			},
			OnLocalDeviceExposed: func(localDeviceID uint32, path string) {
				log.Println("Exposed local device", localDeviceID, "at", path)
			},

			OnLocalAllDevicesRequested: func() {
				log.Println("Requested all local devices")
			},
		},
	)

	if migratedPeer.Wait != nil {
		defer func() {
			defer handlePanics(true)()

			if err := migratedPeer.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := migratedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := migratedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	before := time.Now()

	resumedPeer, err := migratedPeer.Resume(
		ctx,

		*resumeTimeout,
	)

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := resumedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := resumedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Resumed VM in", time.Since(before), "on", peer.VMPath)

	if err := migratedPeer.Wait(); err != nil {
		panic(err)
	}

	if strings.TrimSpace(*laddr) == "" {
		bubbleSignals = true

		select {
		case <-ctx.Done():
			return

		case <-done:
			before = time.Now()

			if err := resumedPeer.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
				panic(err)
			}

			log.Println("Suspend:", time.Since(before))

			log.Println("Shutting down")

			return
		}
	}

	var (
		closeLock sync.Mutex
		closed    bool
	)
	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()
	defer func() {
		closeLock.Lock()
		defer closeLock.Unlock()

		closed = true
	}()

	log.Println("Serving on", lis.Addr())

	// We use `context.Background` here because we want to distinguish between a cancellation and a successful accept
	// We select between `acceptedCtx` and `ctx` on all code paths so we don't leak the context
	acceptedCtx, cancelAcceptedCtx := context.WithCancel(context.Background())
	defer cancelAcceptedCtx()

	var conn net.Conn
	handleGoroutinePanics(true, func() {
		conn, err = lis.Accept()
		if err != nil {
			closeLock.Lock()
			defer closeLock.Unlock()

			if closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
				if err := ctx.Err(); err != nil {
					panic(ctx.Err())
				}

				return
			}

			panic(err)
		}

		cancelAcceptedCtx()
	})

	bubbleSignals = true

	select {
	case <-ctx.Done():
		return

	case <-done:
		before = time.Now()

		if err := resumedPeer.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
			panic(err)
		}

		log.Println("Suspend:", time.Since(before))

		log.Println("Shutting down")

		return

	case <-acceptedCtx.Done():
		break
	}

	defer conn.Close()

	log.Println("Migrating to", conn.RemoteAddr())

	migratablePeer, err := resumedPeer.MakeMigratable(
		ctx,

		*stateExpiry,
		*memoryExpiry,
		*initramfsExpiry,
		*kernelExpiry,
		*diskExpiry,
		*configExpiry,
	)

	if err != nil {
		panic(err)
	}

	defer migratablePeer.Close()

	before = time.Now()
	if err := migratablePeer.MigrateTo(
		ctx,

		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *stateMaxDirtyBlocks,
			MinCycles:      *stateMinCycles,
			MaxCycles:      *stateMaxCycles,

			CycleThrottle: *stateCycleThrottle,

			Serve: *stateServe,
		},
		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *memoryMaxDirtyBlocks,
			MinCycles:      *memoryMinCycles,
			MaxCycles:      *memoryMaxCycles,

			CycleThrottle: *memoryCycleThrottle,

			Serve: *memoryServe,
		},
		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *initramfsMaxDirtyBlocks,
			MinCycles:      *initramfsMinCycles,
			MaxCycles:      *initramfsMaxCycles,

			CycleThrottle: *initramfsCycleThrottle,

			Serve: *initramfsServe,
		},
		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *kernelMaxDirtyBlocks,
			MinCycles:      *kernelMinCycles,
			MaxCycles:      *kernelMaxCycles,

			CycleThrottle: *kernelCycleThrottle,

			Serve: *kernelServe,
		},
		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *diskMaxDirtyBlocks,
			MinCycles:      *diskMinCycles,
			MaxCycles:      *diskMaxCycles,

			CycleThrottle: *diskCycleThrottle,

			Serve: *diskServe,
		},
		roles.MigrateToDeviceConfiguration{
			MaxDirtyBlocks: *configMaxDirtyBlocks,
			MinCycles:      *configMinCycles,
			MaxCycles:      *configMaxCycles,

			CycleThrottle: *configCycleThrottle,

			Serve: *configServe,
		},

		*resumeTimeout,
		*concurrency,

		[]io.Reader{conn},
		[]io.Writer{conn},

		roles.MigrateToHooks{
			OnBeforeSuspend: func() {
				before = time.Now()
			},
			OnAfterSuspend: func() {
				log.Println("Suspend:", time.Since(before))
			},

			OnDeviceSent: func(deviceID uint32, remote bool) {
				if remote {
					log.Println("Sent remote device", deviceID)
				} else {
					log.Println("Sent local device", deviceID)
				}
			},
			OnDeviceAuthoritySent: func(deviceID uint32, remote bool) {
				if remote {
					log.Println("Sent authority for remote device", deviceID)
				} else {
					log.Println("Sent authority for local device", deviceID)
				}
			},
			OnDeviceInitialMigrationProgress: func(deviceID uint32, remote bool, ready, total int) {
				if remote {
					log.Println("Migrated", ready, "of", total, "initial blocks for remote device", deviceID)
				} else {
					log.Println("Migrated", ready, "of", total, "initial blocks for local device", deviceID)
				}
			},
			OnDeviceContinousMigrationProgress: func(deviceID uint32, remote bool, delta int) {
				if remote {
					log.Println("Migrated", delta, "continous blocks for remote device", deviceID)
				} else {
					log.Println("Migrated", delta, "continous blocks for local device", deviceID)
				}
			},
			OnDeviceFinalMigrationProgress: func(deviceID uint32, remote bool, delta int) {
				if remote {
					log.Println("Migrated", delta, "final blocks for remote device", deviceID)
				} else {
					log.Println("Migrated", delta, "final blocks for local device", deviceID)
				}
			},
			OnDeviceMigrationCompleted: func(deviceID uint32, remote bool) {
				if remote {
					log.Println("Completed migration of remote device", deviceID)
				} else {
					log.Println("Completed migration of local device", deviceID)
				}
			},

			OnAllDevicesSent: func() {
				log.Println("Sent all devices")
			},
			OnAllMigrationsCompleted: func() {
				log.Println("Completed all device migrations")
			},
		},
	); err != nil {
		panic(err)
	}

	log.Println("Shutting down")
}
