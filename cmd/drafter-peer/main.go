package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
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

	configBasePath := flag.String("config-base-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config base path")
	diskBasePath := flag.String("disk-base-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk base path")
	initramfsBasePath := flag.String("initramfs-base-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs base path")
	kernelBasePath := flag.String("kernel-base-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel base path")
	memoryBasePath := flag.String("memory-base-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory base path")
	stateBasePath := flag.String("state-base-path", filepath.Join("out", "package", "drafter.drftstate"), "State base path")

	configOverlayPath := flag.String("config-overlay-path", filepath.Join("out", "overlay", "drafter.drftconfig.overlay"), "Config overlay path")
	diskOverlayPath := flag.String("disk-overlay-path", filepath.Join("out", "overlay", "drafter.drftdisk.overlay"), "Disk overlay path")
	initramfsOverlayPath := flag.String("initramfs-overlay-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.overlay"), "initramfs overlay path")
	kernelOverlayPath := flag.String("kernel-overlay-path", filepath.Join("out", "overlay", "drafter.drftkernel.overlay"), "Kernel overlay path")
	memoryOverlayPath := flag.String("memory-overlay-path", filepath.Join("out", "overlay", "drafter.drftmemory.overlay"), "Memory overlay path")
	stateOverlayPath := flag.String("state-overlay-path", filepath.Join("out", "overlay", "drafter.drftstate.overlay"), "State overlay path")

	configStatePath := flag.String("config-state-path", filepath.Join("out", "overlay", "drafter.drftconfig.state"), "Config state path")
	diskStatePath := flag.String("disk-state-path", filepath.Join("out", "overlay", "drafter.drftdisk.state"), "Disk state path")
	initramfsStatePath := flag.String("initramfs-state-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.state"), "initramfs state path")
	kernelStatePath := flag.String("kernel-state-path", filepath.Join("out", "overlay", "drafter.drftkernel.state"), "Kernel state path")
	memoryStatePath := flag.String("memory-state-path", filepath.Join("out", "overlay", "drafter.drftmemory.state"), "Memory state path")
	stateStatePath := flag.String("state-state-path", filepath.Join("out", "overlay", "drafter.drftstate.state"), "State state path")

	stateBlockSizeStorage := flag.Uint("state-block-size-storage", 1024*64, "State block size for storage")
	memoryBlockSizeStorage := flag.Uint("memory-block-size-storage", 1024*64, "Memory block size for storage")
	initramfsBlockSizeStorage := flag.Uint("initramfs-block-size-storage", 1024*64, "initramfs block size for storage")
	kernelBlockSizeStorage := flag.Uint("kernel-block-size-storage", 1024*64, "Kernel block size for storage")
	diskBlockSizeStorage := flag.Uint("disk-block-size-storage", 1024*64, "Disk block size for storage")
	configBlockSizeStorage := flag.Uint("config-block-size-storage", 1024*64, "Config block size for storage")

	stateBlockSizeDevice := flag.Uint64("state-block-size-device", 4096, "State block size for NBD device")
	memoryBlockSizeDevice := flag.Uint64("memory-block-size-device", 4096, "Memory block size for NBD device")
	initramfsBlockSizeDevice := flag.Uint64("initramfs-block-size-device", 4096, "initramfs block size NBD for device")
	kernelBlockSizeDevice := flag.Uint64("kernel-block-size-device", 4096, "Kernel block size for NBD device")
	diskBlockSizeDevice := flag.Uint64("disk-block-size-device", 4096, "Disk block size for NBD device")
	configBlockSizeDevice := flag.Uint64("config-block-size-device", 4096, "Config block size for NBD device")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")

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

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	peer, err := roles.StartPeer(
		ctx,
		context.Background(), // Never give up on rescue operations

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

		config.StateName,
		config.MemoryName,
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

		*stateBasePath,
		*memoryBasePath,
		*initramfsBasePath,
		*kernelBasePath,
		*diskBasePath,
		*configBasePath,

		*stateOverlayPath,
		*memoryOverlayPath,
		*initramfsOverlayPath,
		*kernelOverlayPath,
		*diskOverlayPath,
		*configOverlayPath,

		*stateStatePath,
		*memoryStatePath,
		*initramfsStatePath,
		*kernelStatePath,
		*diskStatePath,
		*configStatePath,

		uint32(*stateBlockSizeStorage),
		uint32(*memoryBlockSizeStorage),
		uint32(*initramfsBlockSizeStorage),
		uint32(*kernelBlockSizeStorage),
		uint32(*diskBlockSizeStorage),
		uint32(*configBlockSizeStorage),

		*stateBlockSizeDevice,
		*memoryBlockSizeDevice,
		*initramfsBlockSizeDevice,
		*kernelBlockSizeDevice,
		*diskBlockSizeDevice,
		*configBlockSizeDevice,

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
				log.Println("Completed all remote migrations")
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

	before = time.Now()

	if err := resumedPeer.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))

	// TODO: Become migratable

	log.Println("Shutting down")
}
