package main

import (
	"context"
	"encoding/json"
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

type CompositeDevices struct {
	Name string `json:"name"`

	Base    string `json:"base"`
	Overlay string `json:"overlay"`
	State   string `json:"state"`

	BlockSize uint32 `json:"blockSize"`

	Expiry time.Duration `json:"expiry"`

	MaxDirtyBlocks int `json:"maxDirtyBlocks"`
	MinCycles      int `json:"minCycles"`
	MaxCycles      int `json:"maxCycles"`

	CycleThrottle time.Duration `json:"cycleThrottle"`
}

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")
	rescueTimeout := flag.Duration("rescue-timeout", time.Minute, "Maximum amount of time to wait for rescue operations")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	defaultDevices, err := json.Marshal([]CompositeDevices{
		{
			Name: roles.StateName,

			Base:    filepath.Join("out", "package", "state.bin"),
			Overlay: filepath.Join("out", "overlay", "state.bin"),
			State:   filepath.Join("out", "state", "state.bin"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},
		{
			Name: roles.MemoryName,

			Base:    filepath.Join("out", "package", "memory.bin"),
			Overlay: filepath.Join("out", "overlay", "memory.bin"),
			State:   filepath.Join("out", "state", "memory.bin"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},

		{
			Name: roles.KernelName,

			Base:    filepath.Join("out", "package", "vmlinux"),
			Overlay: filepath.Join("out", "overlay", "vmlinux"),
			State:   filepath.Join("out", "state", "vmlinux"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},
		{
			Name: roles.DiskName,

			Base:    filepath.Join("out", "package", "rootfs.ext4"),
			Overlay: filepath.Join("out", "overlay", "rootfs.ext4"),
			State:   filepath.Join("out", "state", "rootfs.ext4"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},

		{
			Name: roles.ConfigName,

			Base:    filepath.Join("out", "package", "config.json"),
			Overlay: filepath.Join("out", "overlay", "config.json"),
			State:   filepath.Join("out", "state", "config.json"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},

		{
			Name: "oci",

			Base:    filepath.Join("out", "package", "oci.ext4"),
			Overlay: filepath.Join("out", "overlay", "oci.ext4"),
			State:   filepath.Join("out", "state", "oci.ext4"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,
		},
	})
	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")
	laddr := flag.String("laddr", "localhost:1337", "Local address to listen on (leave empty to disable)")

	concurrency := flag.Int("concurrency", 4096, "Number of concurrent workers to use in migrations")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []CompositeDevices
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
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
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", *raddr)
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		log.Println("Migrating from", conn.RemoteAddr())

		readers = []io.Reader{conn}
		writers = []io.Writer{conn}
	}

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

	migrateFromDevices := []roles.MigrateFromDevice{}
	for _, device := range devices {
		migrateFromDevices = append(migrateFromDevices, roles.MigrateFromDevice{
			Name: device.Name,

			Base:    device.Base,
			Overlay: device.Overlay,
			State:   device.State,

			BlockSize: device.BlockSize,
		})
	}

	migratedPeer, err := peer.MigrateFrom(
		ctx,

		migrateFromDevices,

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
		*rescueTimeout,
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

	makeMigratableDevices := []roles.MakeMigratableDevice{}
	for _, device := range devices {
		makeMigratableDevices = append(makeMigratableDevices, roles.MakeMigratableDevice{
			Name: device.Name,

			Expiry: device.Expiry,
		})
	}

	migratablePeer, err := resumedPeer.MakeMigratable(
		ctx,

		makeMigratableDevices,
	)

	if err != nil {
		panic(err)
	}

	defer migratablePeer.Close()

	migrateToDevices := []roles.MigrateToDevice{}
	for _, device := range devices {
		migrateToDevices = append(migrateToDevices, roles.MigrateToDevice{
			Name: device.Name,

			MaxDirtyBlocks: device.MaxDirtyBlocks,
			MinCycles:      device.MinCycles,
			MaxCycles:      device.MaxCycles,

			CycleThrottle: device.CycleThrottle,
		})
	}

	before = time.Now()
	if err := migratablePeer.MigrateTo(
		ctx,

		migrateToDevices,

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
