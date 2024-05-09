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

	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to")

	nbdBlockSize := flag.Uint64("nbd-block-size", 4096, "NBD block size to use")

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

	conn, err := net.Dial("tcp", *raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

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

		*statePath,
		*memoryPath,
		*initramfsPath,
		*kernelPath,
		*diskPath,
		*configPath,

		[]io.Reader{conn},
		[]io.Writer{conn},

		roles.MigrateFromHooks{
			OnDeviceReceived: func(deviceID uint32, name string) {
				log.Println("Received device", deviceID, "with name", name)
			},
			OnDeviceExposed: func(deviceID uint32, path string) {
				log.Println("Exposed device", deviceID, "at", path)
			},
			OnDeviceAuthorityReceived: func(deviceID uint32) {
				log.Println("Received authority for device", deviceID)
			},
			OnDeviceMigrationCompleted: func(deviceID uint32) {
				log.Println("Completed migration of device", deviceID)
			},

			OnAllDevicesReceived: func() {
				log.Println("Received all devices")
			},
			OnAllMigrationsCompleted: func() {
				log.Println("Completed all migrations")
			},
		},

		*nbdBlockSize,
	)

	if migratedPeer.WaitForMigrationsToComplete != nil {
		defer func() {
			defer handlePanics(true)()

			if err := migratedPeer.WaitForMigrationsToComplete(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	handleGoroutinePanics(true, func() {
		if err := migratedPeer.WaitForMigrationsToComplete(); err != nil {
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

	if err := migratedPeer.WaitForMigrationsToComplete(); err != nil {
		panic(err)
	}

	// TODO: Become migratable

	log.Println("Shutting down")
}
