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

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
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

	MakeMigratable bool `json:"makeMigratable"`
	Shared         bool `json:"shared"`
}

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "chroot base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")
	rescueTimeout := flag.Duration("rescue-timeout", time.Minute, "Maximum amount of time to wait for rescue operations")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	experimentalMapPrivate := flag.Bool("experimental-map-private", false, "(Experimental) Whether to use MAP_PRIVATE for memory and state devices")
	experimentalMapPrivateStateOutput := flag.String("experimental-map-private-state-output", "", "(Experimental) Path to write the local changes to the shared state to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")
	experimentalMapPrivateMemoryOutput := flag.String("experimental-map-private-memory-output", "", "(Experimental) Path to write the local changes to the shared memory to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")

	defaultDevices, err := json.Marshal([]CompositeDevices{
		{
			Name: packager.StateName,

			Base:    filepath.Join("out", "package", "state.bin"),
			Overlay: filepath.Join("out", "overlay", "state.bin"),
			State:   filepath.Join("out", "state", "state.bin"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,

			MakeMigratable: true,
			Shared:         false,
		},
		{
			Name: packager.MemoryName,

			Base:    filepath.Join("out", "package", "memory.bin"),
			Overlay: filepath.Join("out", "overlay", "memory.bin"),
			State:   filepath.Join("out", "state", "memory.bin"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,

			MakeMigratable: true,
			Shared:         false,
		},

		{
			Name: packager.KernelName,

			Base:    filepath.Join("out", "package", "vmlinux"),
			Overlay: filepath.Join("out", "overlay", "vmlinux"),
			State:   filepath.Join("out", "state", "vmlinux"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,

			MakeMigratable: true,
			Shared:         false,
		},
		{
			Name: packager.DiskName,

			Base:    filepath.Join("out", "package", "rootfs.ext4"),
			Overlay: filepath.Join("out", "overlay", "rootfs.ext4"),
			State:   filepath.Join("out", "state", "rootfs.ext4"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,

			MakeMigratable: true,
			Shared:         false,
		},

		{
			Name: packager.ConfigName,

			Base:    filepath.Join("out", "package", "config.json"),
			Overlay: filepath.Join("out", "overlay", "config.json"),
			State:   filepath.Join("out", "state", "config.json"),

			BlockSize: 1024 * 64,

			Expiry: time.Second,

			MaxDirtyBlocks: 200,
			MinCycles:      5,
			MaxCycles:      20,

			CycleThrottle: time.Millisecond * 500,

			MakeMigratable: true,
			Shared:         false,
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

			MakeMigratable: true,
			Shared:         false,
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

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

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
		conn, err := (&net.Dialer{}).DialContext(goroutineManager.Context(), "tcp", *raddr)
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		log.Println("Migrating from", conn.RemoteAddr())

		readers = []io.Reader{conn}
		writers = []io.Writer{conn}
	}

	p, err := peer.StartPeer[struct{}, ipc.AgentServerRemote[struct{}]](
		goroutineManager.Context(),
		context.Background(), // Never give up on rescue operations

		snapshotter.HypervisorConfiguration{
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

		packager.StateName,
		packager.MemoryName,
	)

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := p.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := p.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := p.Wait(); err != nil {
			panic(err)
		}
	})

	migrateFromDevices := []peer.MigrateFromDevice[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{}
	for _, device := range devices {
		migrateFromDevices = append(migrateFromDevices, peer.MigrateFromDevice[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Name: device.Name,

			Base:    device.Base,
			Overlay: device.Overlay,
			State:   device.State,

			BlockSize: device.BlockSize,

			Shared: device.Shared,
		})
	}

	migratedPeer, err := p.MigrateFrom(
		goroutineManager.Context(),

		migrateFromDevices,

		readers,
		writers,

		mounter.MigrateFromHooks{
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

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := migratedPeer.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := migratedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := migratedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	before := time.Now()

	resumedPeer, err := migratedPeer.Resume(
		goroutineManager.Context(),

		*resumeTimeout,
		*rescueTimeout,

		struct{}{},

		runner.SnapshotLoadConfiguration{
			ExperimentalMapPrivate: *experimentalMapPrivate,

			ExperimentalMapPrivateStateOutput:  *experimentalMapPrivateStateOutput,
			ExperimentalMapPrivateMemoryOutput: *experimentalMapPrivateMemoryOutput,
		},
	)

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := resumedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := resumedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Resumed VM in", time.Since(before), "on", p.VMPath)

	if err := migratedPeer.Wait(); err != nil {
		panic(err)
	}

	if strings.TrimSpace(*laddr) == "" {
		bubbleSignals = true

		select {
		case <-goroutineManager.Context().Done():
			return

		case <-done:
			before = time.Now()

			if err := resumedPeer.SuspendAndCloseAgentServer(goroutineManager.Context(), *resumeTimeout); err != nil {
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
	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		closeLock.Lock()

		closed = true

		closeLock.Unlock()

		if err := lis.Close(); err != nil {
			panic(err)
		}
	}()

	log.Println("Serving on", lis.Addr())

	var (
		ready       = make(chan struct{})
		signalReady = sync.OnceFunc(func() {
			close(ready) // We can safely close() this channel since the caller only runs once/is `sync.OnceFunc`d
		})
	)

	var conn net.Conn
	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		conn, err = lis.Accept()
		if err != nil {
			closeLock.Lock()
			defer closeLock.Unlock()

			if closed && errors.Is(err, net.ErrClosed) { // Don't treat closed errors as errors if we closed the connection
				if err := goroutineManager.Context().Err(); err != nil {
					panic(err)
				}

				return
			}

			panic(err)
		}

		signalReady()
	})

	bubbleSignals = true

	select {
	case <-goroutineManager.Context().Done():
		return

	case <-done:
		before = time.Now()

		if err := resumedPeer.SuspendAndCloseAgentServer(goroutineManager.Context(), *resumeTimeout); err != nil {
			panic(err)
		}

		log.Println("Suspend:", time.Since(before))

		log.Println("Shutting down")

		return

	case <-ready:
		break
	}

	defer conn.Close()

	log.Println("Migrating to", conn.RemoteAddr())

	makeMigratableDevices := []mounter.MakeMigratableDevice{}
	for _, device := range devices {
		if !device.MakeMigratable || device.Shared {
			continue
		}

		makeMigratableDevices = append(makeMigratableDevices, mounter.MakeMigratableDevice{
			Name: device.Name,

			Expiry: device.Expiry,
		})
	}

	migratablePeer, err := resumedPeer.MakeMigratable(
		goroutineManager.Context(),

		makeMigratableDevices,
	)

	if err != nil {
		panic(err)
	}

	defer migratablePeer.Close()

	migrateToDevices := []mounter.MigrateToDevice{}
	for _, device := range devices {
		if !device.MakeMigratable || device.Shared {
			continue
		}

		migrateToDevices = append(migrateToDevices, mounter.MigrateToDevice{
			Name: device.Name,

			MaxDirtyBlocks: device.MaxDirtyBlocks,
			MinCycles:      device.MinCycles,
			MaxCycles:      device.MaxCycles,

			CycleThrottle: device.CycleThrottle,
		})
	}

	before = time.Now()
	if err := migratablePeer.MigrateTo(
		goroutineManager.Context(),

		migrateToDevices,

		*resumeTimeout,
		*concurrency,

		[]io.Reader{conn},
		[]io.Writer{conn},

		peer.MigrateToHooks{
			OnBeforeGetDirtyBlocks: func(deviceID uint32, remote bool) {
				if remote {
					log.Println("Getting dirty blocks for remote device", deviceID)
				} else {
					log.Println("Getting dirty blocks for local device", deviceID)
				}
			},

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
