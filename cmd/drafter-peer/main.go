package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	iconfig "github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/expose"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
	"golang.org/x/sys/unix"
)

type CustomEventType byte

const (
	EventCustomPassAuthority  = CustomEventType(0)
	EventCustomAllDevicesSent = CustomEventType(1)
)

var (
	errUnknownResourceName = errors.New("unknown resource name")
)

type resource struct {
	name      string
	blockSize uint32
	size      uint64

	base    string
	overlay string
	state   string

	exp     storage.ExposedStorage
	storage storage.StorageProvider

	serve bool

	maxDirtyBlocks int
	minCycles      int
	maxCycles      int
}

type exposedResource struct {
	resource resource

	exp         storage.ExposedStorage
	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	configBasePath := flag.String("config-base-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config base path")
	diskBasePath := flag.String("disk-base-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk base path")
	initramfsBasePath := flag.String("initramfs-base-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs base path")
	kernelBasePath := flag.String("kernel-base-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel base path")
	memoryBasePath := flag.String("memory-base-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory base path")
	stateBasePath := flag.String("state-base-path", filepath.Join("out", "package", "drafter.drftstate"), "State base path")

	raddr := flag.String("raddr", "", "Remote Silo address (connect use only) (set to empty value to serve instead)")

	laddr := flag.String("laddr", ":1337", "Local Silo address (serve use only)")
	blockSize := flag.Uint("block-size", 1024*64, "Block size to use (serve use only)")

	configOverlayPath := flag.String("config-overlay-path", filepath.Join("out", "overlay", "drafter.drftconfig.overlay"), "Config overlay path (serve use only)")
	diskOverlayPath := flag.String("disk-overlay-path", filepath.Join("out", "overlay", "drafter.drftdisk.overlay"), "Disk overlay path (serve use only)")
	initramfsOverlayPath := flag.String("initramfs-overlay-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.overlay"), "initramfs overlay path (serve use only)")
	kernelOverlayPath := flag.String("kernel-overlay-path", filepath.Join("out", "overlay", "drafter.drftkernel.overlay"), "Kernel overlay path (serve use only)")
	memoryOverlayPath := flag.String("memory-overlay-path", filepath.Join("out", "overlay", "drafter.drftmemory.overlay"), "Memory overlay path (serve use only)")
	stateOverlayPath := flag.String("state-overlay-path", filepath.Join("out", "overlay", "drafter.drftstate.overlay"), "State overlay path (serve use only)")

	configStatePath := flag.String("config-state-path", filepath.Join("out", "overlay", "drafter.drftconfig.state"), "Config state path (serve use only)")
	diskStatePath := flag.String("disk-state-path", filepath.Join("out", "overlay", "drafter.drftdisk.state"), "Disk state path (serve use only)")
	initramfsStatePath := flag.String("initramfs-state-path", filepath.Join("out", "overlay", "drafter.drftinitramfs.state"), "initramfs state path (serve use only)")
	kernelStatePath := flag.String("kernel-state-path", filepath.Join("out", "overlay", "drafter.drftkernel.state"), "Kernel state path (serve use only)")
	memoryStatePath := flag.String("memory-state-path", filepath.Join("out", "overlay", "drafter.drftmemory.state"), "Memory state path (serve use only)")
	stateStatePath := flag.String("state-state-path", filepath.Join("out", "overlay", "drafter.drftstate.state"), "State state path (serve use only)")

	configServe := flag.Bool("config-serve", true, "Whether to serve the config (serve use only)")
	diskServe := flag.Bool("disk-serve", true, "Whether to serve the disk (serve use only)")
	initramfsServe := flag.Bool("initramfs-serve", true, "Whether to serve the initramfs (serve use only)")
	kernelServe := flag.Bool("kernel-serve", true, "Whether to serve the kernel (serve use only)")
	memoryServe := flag.Bool("memory-serve", true, "Whether to serve the memory (serve use only)")
	stateServe := flag.Bool("state-serve", true, "Whether to serve the state (serve use only)")

	configMaxDirtyBlocks := flag.Int("config-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for config (serve use only)")
	diskMaxDirtyBlocks := flag.Int("disk-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for disk (serve use only)")
	initramfsMaxDirtyBlocks := flag.Int("initramfs-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for initramfs (serve use only)")
	kernelMaxDirtyBlocks := flag.Int("kernel-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for kernel (serve use only)")
	memoryMaxDirtyBlocks := flag.Int("memory-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for memory (serve use only)")
	stateMaxDirtyBlocks := flag.Int("state-max-dirty-blocks", 200, "Maximum amount of dirty blocks per cycle after which to transfer authority for state (serve use only)")

	configMinCycles := flag.Int("config-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for config (serve use only)")
	diskMinCycles := flag.Int("disk-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for disk (serve use only)")
	initramfsMinCycles := flag.Int("initramfs-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for initramfs (serve use only)")
	kernelMinCycles := flag.Int("kernel-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for kernel (serve use only)")
	memoryMinCycles := flag.Int("memory-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for memory (serve use only)")
	stateMinCycles := flag.Int("state-min-cycles", 5, "Minimum amount of cycles with dirty blocks below the block threshold after which to transfer authority for state (serve use only)")

	configMaxCycles := flag.Int("config-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for config (serve use only)")
	diskMaxCycles := flag.Int("disk-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for disk (serve use only)")
	initramfsMaxCycles := flag.Int("initramfs-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for initramfs (serve use only)")
	kernelMaxCycles := flag.Int("kernel-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for kernel (serve use only)")
	memoryMaxCycles := flag.Int("memory-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for memory (serve use only)")
	stateMaxCycles := flag.Int("state-max-cycles", 10, "Maximum amount of cycles to check for dirty blocks after which to transfer authority for state (serve use only)")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

	flag.Parse()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
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

	runner, err := roles.StartRunner(
		ctx,
		context.Background(), // Never give up on rescue operations

		iconfig.HypervisorConfiguration{
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

		iconfig.StateName,
		iconfig.MemoryName,
	)

	if runner.Wait != nil {
		defer func() {
			defer handlePanics(true)()

			if err := runner.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := runner.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := runner.Wait(); err != nil {
			panic(err)
		}
	})

	var packageConfig iconfig.PackageConfiguration

	var (
		resources        []resource
		exposedResources = []exposedResource{}
		resumedRunner    *roles.ResumedRunner
	)
	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		if resumedRunner != nil {
			if err := resumedRunner.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
				panic(err)
			}
		}

		if err := runner.Close(); err != nil {
			panic(err)
		}

		if len(exposedResources) == 0 {
			for _, res := range resources {
				if res.exp != nil {
					if err := res.exp.Shutdown(); err != nil {
						panic(err)
					}
				}

				if res.storage != nil {
					if err := res.storage.Close(); err != nil {
						panic(err)
					}
				}
			}
		} else {
			for _, eres := range exposedResources {
				if err := eres.exp.Shutdown(); err != nil {
					panic(err)
				}

				if err := eres.storage.Close(); err != nil {
					panic(err)
				}
			}
		}

		os.Exit(0)
	}()

	resourceConfigurations := []resource{
		{
			name:      iconfig.ConfigName,
			blockSize: uint32(*blockSize),

			base:    *configBasePath,
			overlay: *configOverlayPath,
			state:   *configStatePath,

			serve: *configServe,

			maxDirtyBlocks: *configMaxDirtyBlocks,
			minCycles:      *configMinCycles,
			maxCycles:      *configMaxCycles,
		},
		{
			name:      iconfig.DiskName,
			blockSize: uint32(*blockSize),

			base:    *diskBasePath,
			overlay: *diskOverlayPath,
			state:   *diskStatePath,

			serve: *diskServe,

			maxDirtyBlocks: *diskMaxDirtyBlocks,
			minCycles:      *diskMinCycles,
			maxCycles:      *diskMaxCycles,
		},
		{
			name:      iconfig.InitramfsName,
			blockSize: uint32(*blockSize),

			base:    *initramfsBasePath,
			overlay: *initramfsOverlayPath,
			state:   *initramfsStatePath,

			serve: *initramfsServe,

			maxDirtyBlocks: *initramfsMaxDirtyBlocks,
			minCycles:      *initramfsMinCycles,
			maxCycles:      *initramfsMaxCycles,
		},
		{
			name:      iconfig.KernelName,
			blockSize: uint32(*blockSize),

			base:    *kernelBasePath,
			overlay: *kernelOverlayPath,
			state:   *kernelStatePath,

			serve: *kernelServe,

			maxDirtyBlocks: *kernelMaxDirtyBlocks,
			minCycles:      *kernelMinCycles,
			maxCycles:      *kernelMaxCycles,
		},
		{
			name:      iconfig.MemoryName,
			blockSize: uint32(*blockSize),

			base:    *memoryBasePath,
			overlay: *memoryOverlayPath,
			state:   *memoryStatePath,

			serve: *memoryServe,

			maxDirtyBlocks: *memoryMaxDirtyBlocks,
			minCycles:      *memoryMinCycles,
			maxCycles:      *memoryMaxCycles,
		},
		{
			name:      iconfig.StateName,
			blockSize: uint32(*blockSize),

			base:    *stateBasePath,
			overlay: *stateOverlayPath,
			state:   *stateStatePath,

			serve: *stateServe,

			maxDirtyBlocks: *stateMaxDirtyBlocks,
			minCycles:      *stateMinCycles,
			maxCycles:      *stateMaxCycles,
		},
	}

	if strings.TrimSpace(*raddr) == "" {
		resources = resourceConfigurations
		for _, res := range resources {
			// TODO: Decompose this into func
			if err := os.MkdirAll(filepath.Dir(res.overlay), os.ModePerm); err != nil {
				panic(err)
			}

			if err := os.MkdirAll(filepath.Dir(res.base), os.ModePerm); err != nil {
				panic(err)
			}

			stat, err := os.Stat(res.base)
			if err != nil {
				panic(err)
			}
			res.size = uint64(stat.Size())

			var (
				src storage.StorageProvider
				exp storage.ExposedStorage
			)
			if strings.TrimSpace(res.overlay) == "" {
				src, exp, err = device.NewDevice(&config.DeviceSchema{
					Name:      res.name,
					System:    "file",
					Location:  res.base,
					Size:      fmt.Sprintf("%v", res.size),
					BlockSize: fmt.Sprintf("%v", res.blockSize),
					Expose:    true,
				})
			} else {
				src, exp, err = device.NewDevice(&config.DeviceSchema{
					Name:      res.name,
					System:    "sparsefile",
					Location:  res.overlay,
					Size:      fmt.Sprintf("%v", res.size),
					BlockSize: fmt.Sprintf("%v", res.blockSize),
					Expose:    true,
					ROSource: &config.DeviceSchema{
						Name:     res.state,
						System:   "file",
						Location: res.base,
						Size:     fmt.Sprintf("%v", res.size),
					},
				})
			}
			if err != nil {
				panic(err)
			}
			defer src.Close()
			defer exp.Shutdown()
			defer runner.Close()

			devicePath := filepath.Join("/dev", exp.Device())

			log.Println("Exposed", devicePath, "for", res.name)

			if res.name == iconfig.ConfigName {
				configFile, err := os.Open(devicePath)
				if err != nil {
					panic(err)
				}
				defer configFile.Close()

				if err := json.NewDecoder(configFile).Decode(&packageConfig); err != nil {
					panic(err)
				}

				if err := configFile.Close(); err != nil {
					panic(err)
				}
			}

			info, err := os.Stat(devicePath)
			if err != nil {
				panic(err)
			}

			deviceStat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				panic(errors.New("could not get NBD device stat"))
			}

			major := uint64(deviceStat.Rdev / 256)
			minor := uint64(deviceStat.Rdev % 256)

			dev := int((major << 8) | minor)

			if err := unix.Mknod(filepath.Join(runner.VMPath, res.name), unix.S_IFBLK|0666, dev); err != nil {
				panic(err)
			}

			if !res.serve {
				continue
			}

			metrics := modules.NewMetrics(src)
			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(res.blockSize))
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(res.blockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			defer storage.Unlock()

			exp.SetProvider(storage)

			totalBlocks := (int(storage.Size()) + int(res.blockSize) - 1) / int(res.blockSize)

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			orderer.AddAll()

			exposedResources = append(exposedResources, exposedResource{
				exp:         exp,
				resource:    res,
				storage:     storage,
				orderer:     orderer,
				totalBlocks: totalBlocks,
				dirtyRemote: dirtyRemote,
			})
		}

		log.Println("Resuming VM")

		before := time.Now()

		resumedRunner, err = runner.Resume(ctx, *resumeTimeout, packageConfig.AgentVSockPort)
		if err != nil {
			panic(err)
		}

		log.Println("Resume:", time.Since(before))
	} else {
		conn, err := net.Dial("tcp", *raddr)
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		log.Println("Migrating from", conn.RemoteAddr())

		var (
			allDevicesReceivedWg  sync.WaitGroup
			devicesReadyWg        sync.WaitGroup
			migrationsCompletedWg sync.WaitGroup
		)

		allDevicesReceivedWg.Add(1)
		setAllDevicesReceived := sync.OnceFunc(func() {
			allDevicesReceivedWg.Done()
		})

		resources := []resource{}
		pro := protocol.NewProtocolRW(
			ctx,
			[]io.Reader{conn},
			[]io.Writer{conn},
			func(p protocol.Protocol, u uint32) {
				var (
					dst   *protocol.FromProtocol
					local *waitingcache.WaitingCacheLocal
				)
				dst = protocol.NewFromProtocol(
					u,
					func(di *packets.DevInfo) storage.StorageProvider {
						var (
							stPath  = ""
							stServe = false
						)
						switch di.Name {
						case iconfig.ConfigName:
							stPath = *configBasePath
							stServe = *configServe

						case iconfig.DiskName:
							stPath = *diskBasePath
							stServe = *diskServe

						case iconfig.InitramfsName:
							stPath = *initramfsBasePath
							stServe = *initramfsServe

						case iconfig.KernelName:
							stPath = *kernelBasePath
							stServe = *kernelServe

						case iconfig.MemoryName:
							stPath = *memoryBasePath
							stServe = *memoryServe

						case iconfig.StateName:
							stPath = *stateBasePath
							stServe = *stateServe
						}

						if strings.TrimSpace(stPath) == "" {
							panic(errUnknownResourceName)
						}

						devicesReadyWg.Add(1)
						migrationsCompletedWg.Add(1)

						if err := os.MkdirAll(filepath.Dir(stPath), os.ModePerm); err != nil {
							panic(err)
						}

						st, err := sources.NewFileStorageCreate(stPath, int64(di.Size))
						if err != nil {
							panic(err)
						}

						var remote *waitingcache.WaitingCacheRemote
						local, remote = waitingcache.NewWaitingCache(st, int(di.Block_size))
						local.NeedAt = func(offset int64, length int32) {
							dst.NeedAt(offset, length)
						}
						local.DontNeedAt = func(offset int64, length int32) {
							dst.DontNeedAt(offset, length)
						}

						exp := expose.NewExposedStorageNBDNL(local, 1, 0, local.Size(), 4096, true)

						resources = append(resources, resource{
							name:      di.Name,
							blockSize: di.Block_size,
							size:      di.Size,
							exp:       exp,
							storage:   local,
							serve:     stServe,
						})

						if err := exp.Init(); err != nil {
							panic(err)
						}

						devicePath := filepath.Join("/dev", exp.Device())

						log.Println("Exposed", devicePath, "for", di.Name)

						info, err := os.Stat(devicePath)
						if err != nil {
							panic(err)
						}

						deviceStat, ok := info.Sys().(*syscall.Stat_t)
						if !ok {
							panic(errors.New("could not get NBD device stat"))
						}

						major := uint64(deviceStat.Rdev / 256)
						minor := uint64(deviceStat.Rdev % 256)

						dev := int((major << 8) | minor)

						if err := unix.Mknod(filepath.Join(runner.VMPath, di.Name), unix.S_IFBLK|0666, dev); err != nil {
							panic(err)
						}

						return remote
					},
					p,
				)

				go func() {
					if err := dst.HandleReadAt(); err != nil {
						panic(err)
					}
				}()

				go func() {
					if err := dst.HandleWriteAt(); err != nil {
						panic(err)
					}
				}()

				go func() {
					if err := dst.HandleDevInfo(); err != nil {
						panic(err)
					}
				}()

				go func() {
					if err := dst.HandleEvent(func(e *packets.Event) {
						switch e.Type {
						case packets.EventCustom:
							switch e.CustomType {
							case byte(EventCustomPassAuthority):
								devicesReadyWg.Done()

							case byte(EventCustomAllDevicesSent):
								setAllDevicesReceived()
							}

						case packets.EventCompleted:
							migrationsCompletedWg.Done()
						}
					}); err != nil {
						panic(err)
					}
				}()

				go func() {
					if err := dst.HandleDirtyList(func(blocks []uint) {
						if local != nil {
							local.DirtyBlocks(blocks)
						}
					}); err != nil {
						panic(err)
					}
				}()
			})
		defer func() {
			_ = runner.Close()

			for _, res := range resources {
				if err := res.exp.Shutdown(); err != nil {
					panic(err)
				}

				if err := res.storage.Close(); err != nil {
					panic(err)
				}
			}
		}()

		go func() {
			if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
				panic(err)
			}
		}()

		allDevicesReceivedWg.Wait()

		for _, res := range resourceConfigurations {
			if slices.ContainsFunc(
				resources,
				func(r resource) bool {
					return res.name == r.name
				},
			) {
				continue
			}

			// TODO: Decompose this into func
			if err := os.MkdirAll(filepath.Dir(res.overlay), os.ModePerm); err != nil {
				panic(err)
			}

			if err := os.MkdirAll(filepath.Dir(res.base), os.ModePerm); err != nil {
				panic(err)
			}

			stat, err := os.Stat(res.base)
			if err != nil {
				panic(err)
			}
			res.size = uint64(stat.Size())

			var (
				src storage.StorageProvider
				exp storage.ExposedStorage
			)
			if strings.TrimSpace(res.overlay) == "" {
				src, exp, err = device.NewDevice(&config.DeviceSchema{
					Name:      res.name,
					System:    "file",
					Location:  res.base,
					Size:      fmt.Sprintf("%v", res.size),
					BlockSize: fmt.Sprintf("%v", res.blockSize),
					Expose:    true,
				})
			} else {
				src, exp, err = device.NewDevice(&config.DeviceSchema{
					Name:      res.name,
					System:    "sparsefile",
					Location:  res.overlay,
					Size:      fmt.Sprintf("%v", res.size),
					BlockSize: fmt.Sprintf("%v", res.blockSize),
					Expose:    true,
					ROSource: &config.DeviceSchema{
						Name:     res.state,
						System:   "file",
						Location: res.base,
						Size:     fmt.Sprintf("%v", res.size),
					},
				})
			}
			if err != nil {
				panic(err)
			}
			defer src.Close()
			defer exp.Shutdown()
			defer runner.Close()

			devicePath := filepath.Join("/dev", exp.Device())

			log.Println("Exposed", devicePath, "for", res.name)

			if res.name == iconfig.ConfigName {
				configFile, err := os.Open(devicePath)
				if err != nil {
					panic(err)
				}
				defer configFile.Close()

				if err := json.NewDecoder(configFile).Decode(&packageConfig); err != nil {
					panic(err)
				}

				if err := configFile.Close(); err != nil {
					panic(err)
				}
			}

			info, err := os.Stat(devicePath)
			if err != nil {
				panic(err)
			}

			deviceStat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				panic(roles.ErrCouldNotGetDeviceStat)
			}

			major := uint64(deviceStat.Rdev / 256)
			minor := uint64(deviceStat.Rdev % 256)

			dev := int((major << 8) | minor)

			if err := unix.Mknod(filepath.Join(runner.VMPath, res.name), unix.S_IFBLK|0666, dev); err != nil {
				panic(err)
			}

			metrics := modules.NewMetrics(src)
			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(res.blockSize))
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(res.blockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			defer storage.Unlock()

			exp.SetProvider(storage)

			totalBlocks := (int(storage.Size()) + int(res.blockSize) - 1) / int(res.blockSize)

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			orderer.AddAll()

			exposedResources = append(exposedResources, exposedResource{
				exp:         exp,
				resource:    res,
				storage:     storage,
				orderer:     orderer,
				totalBlocks: totalBlocks,
				dirtyRemote: dirtyRemote,
			})
		}

		devicesReadyWg.Wait()

		log.Println("Resuming VM")

		configFile, err := os.Open(filepath.Join(runner.VMPath, iconfig.ConfigName))
		if err != nil {
			panic(err)
		}
		defer configFile.Close()

		var packageConfig iconfig.PackageConfiguration
		if err := json.NewDecoder(configFile).Decode(&packageConfig); err != nil {
			panic(err)
		}

		if err := configFile.Close(); err != nil {
			panic(err)
		}

		before := time.Now()

		resumedRunner, err = runner.Resume(ctx, *resumeTimeout, packageConfig.AgentVSockPort)
		if err != nil {
			panic(err)
		}

		log.Println("Resume:", time.Since(before))

		migrationsCompletedWg.Wait()

		for _, res := range resources {
			if !res.serve {
				continue
			}

			metrics := modules.NewMetrics(res.storage)
			dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(res.blockSize))
			monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(res.blockSize), 10*time.Second)

			storage := modules.NewLockable(monitor)
			defer storage.Unlock()

			res.exp.SetProvider(storage)

			totalBlocks := (int(storage.Size()) + int(res.blockSize) - 1) / int(res.blockSize)

			orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
			orderer.AddAll()

			exposedResources = append(exposedResources, exposedResource{
				resource:    res,
				exp:         res.exp,
				storage:     storage,
				orderer:     orderer,
				totalBlocks: totalBlocks,
				dirtyRemote: dirtyRemote,
			})
		}
	}

	becomeMigratable := false
	for _, res := range exposedResources {
		if res.resource.serve {
			becomeMigratable = true

			break
		}
	}

	if !becomeMigratable {
		log.Println("Completed all migrations, idling")

		select {}
	}

	log.Println("Completed all migrations, becoming migratable")

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Serving on", lis.Addr())

	conn, err := lis.Accept()
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating to", conn.RemoteAddr())

	pro := protocol.NewProtocolRW(ctx, []io.Reader{conn}, []io.Writer{conn}, nil)

	go func() {
		if err := pro.Handle(); err != nil {
			panic(err)
		}
	}()

	var (
		suspendWg   sync.WaitGroup
		suspendedWg sync.WaitGroup
	)

	suspendWg.Add(len(exposedResources))
	suspendVM := false

	suspendedWg.Add(1)
	go func() {
		suspendWg.Wait()

		log.Println("Suspending VM")

		before := time.Now()

		if err := resumedRunner.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
			panic(err)
		}

		log.Println("Suspend:", time.Since(before))

		suspendedWg.Done()
	}()

	var completedWg sync.WaitGroup
	completedWg.Add(len(exposedResources))

	var devicesLeftToSend atomic.Int32

	for i, eres := range exposedResources {
		go func(i int, eres exposedResource) {
			defer completedWg.Done()

			dst := protocol.NewToProtocol(eres.storage.Size(), uint32(i), pro)

			if err := dst.SendDevInfo(eres.resource.name, eres.resource.blockSize); err != nil {
				panic(err)
			}
			devicesLeftToSend.Add(1)

			if devicesLeftToSend.Load() >= int32(len(exposedResources)) {
				go func() {
					if err := dst.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(EventCustomAllDevicesSent),
					}); err != nil {
						panic(err)
					}
				}()
			}

			go func() {
				if err := dst.HandleNeedAt(func(offset int64, length int32) {
					// Prioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(eres.storage.Size()) {
						endOffset = uint64(eres.storage.Size())
					}

					startBlock := int(offset / int64(eres.resource.blockSize))
					endBlock := int((endOffset-1)/uint64(eres.resource.blockSize)) + 1
					for b := startBlock; b < endBlock; b++ {
						eres.orderer.PrioritiseBlock(b)
					}
				}); err != nil {
					panic(err)
				}
			}()

			go func() {
				if err := dst.HandleDontNeedAt(func(offset int64, length int32) {
					// Deprioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(eres.storage.Size()) {
						endOffset = uint64(eres.storage.Size())
					}

					startBlock := int(offset / int64(eres.storage.Size()))
					endBlock := int((endOffset-1)/uint64(eres.storage.Size())) + 1
					for b := startBlock; b < endBlock; b++ {
						eres.orderer.Remove(b)
					}
				}); err != nil {
					panic(err)
				}
			}()

			cfg := migrator.NewMigratorConfig().WithBlockSize(int(eres.resource.blockSize))
			cfg.Concurrency = map[int]int{
				storage.BlockTypeAny:      *concurrency,
				storage.BlockTypeStandard: *concurrency,
				storage.BlockTypeDirty:    *concurrency,
				storage.BlockTypePriority: *concurrency,
			}
			cfg.Locker_handler = func() {
				if err := dst.SendEvent(&packets.Event{
					Type: packets.EventPreLock,
				}); err != nil {
					panic(err)
				}

				eres.storage.Lock()

				if err := dst.SendEvent(&packets.Event{
					Type: packets.EventPostLock,
				}); err != nil {
					panic(err)
				}

			}
			cfg.Unlocker_handler = func() {
				if err := dst.SendEvent(&packets.Event{
					Type: packets.EventPreUnlock,
				}); err != nil {
					panic(err)
				}

				eres.storage.Unlock()

				if err := dst.SendEvent(&packets.Event{
					Type: packets.EventPostUnlock,
				}); err != nil {
					panic(err)
				}
			}
			cfg.Progress_handler = func(p *migrator.MigrationProgress) {
				// log.Printf("%v/%v", p.ReadyBlocks, p.TotalBlocks)
			}

			mig, err := migrator.NewMigrator(eres.dirtyRemote, dst, eres.orderer, cfg)
			if err != nil {
				panic(err)
			}

			log.Println("Migrating", eres.totalBlocks, "blocks for", eres.resource.name)

			if err := mig.Migrate(eres.totalBlocks); err != nil {
				panic(err)
			}

			if err := mig.WaitForCompletion(); err != nil {
				panic(err)
			}

			// 1) Get dirty blocks. If the delta is small enough:
			// 2) Mark VM to be suspended on the next iteration
			// 3) Send list of dirty changes
			// 4) Migrate blocks & jump back to start of loop
			// 5) Suspend & `msync` VM since it's been marked
			// 6) Mark VM not to be suspended on the next iteration
			// 7) Get dirty blocks
			// 8) Send dirty list
			// 9) Resume VM on remote (in background) - we need to signal this
			// 10) Migrate blocks & jump back to start of loop
			// 11) Get dirty blocks returns `nil`, so break out of loop

			suspendedVM := false
			passAuthority := false

			var backgroundMigrationInProgress sync.WaitGroup

			subsequentSyncs := 0
			for {
				if suspendVM && !suspendedVM {
					suspendedVM = true

					suspendWg.Done()

					mig.Unlock()

					suspendedWg.Wait()

					passAuthority = true

					backgroundMigrationInProgress.Wait()
				}

				if !suspendVM && eres.resource.name == iconfig.MemoryName {
					if err := resumedRunner.Msync(ctx); err != nil {
						panic(err)
					}
				}

				blocks := mig.GetLatestDirty()
				if blocks == nil {
					mig.Unlock()
				}
				if suspendedVM && !passAuthority {
					break
				}

				// Below threshold; let's suspend the VM here and resume it over there
				if len(blocks) <= eres.resource.maxDirtyBlocks && !suspendedVM { // && len(blocks) > 0
					if eres.resource.name == iconfig.MemoryName {
						subsequentSyncs++

						if subsequentSyncs > eres.resource.minCycles || subsequentSyncs > eres.resource.maxCycles {
							suspendVM = true
						} else {
							time.Sleep(time.Millisecond * 500)
						}
					}
				}

				if blocks != nil {
					log.Println("Continously migrating", len(blocks), "blocks for", eres.resource.name)
				}

				if blocks != nil {
					if err := dst.DirtyList(blocks); err != nil {
						panic(err)
					}
				}

				if passAuthority {
					passAuthority = false

					log.Println("Passing authority to destination for", eres.resource.name)

					if err := dst.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(EventCustomPassAuthority),
					}); err != nil {
						panic(err)
					}
				}

				if suspendVM && !suspendedVM && blocks != nil {
					go func() {
						defer backgroundMigrationInProgress.Done()
						backgroundMigrationInProgress.Add(1)

						if err := mig.MigrateDirty(blocks); err != nil {
							panic(err)
						}
					}()
				} else {
					if err := mig.MigrateDirty(blocks); err != nil {
						panic(err)
					}
				}
			}

			if err := mig.WaitForCompletion(); err != nil {
				panic(err)
			}

			if err := dst.SendEvent(&packets.Event{
				Type: packets.EventCompleted,
			}); err != nil {
				panic(err)
			}
		}(i, eres)
	}

	completedWg.Wait()

	log.Println("Completed all migrations, shutting down")
}
