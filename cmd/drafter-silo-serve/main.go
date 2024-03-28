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
	"sync"
	"syscall"
	"time"

	iconfig "github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
	"golang.org/x/sys/unix"
)

const (
	blockSize = 1024 * 64
)

type resource struct {
	name string
	path string
}

type exposedResource struct {
	resource    resource
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

	flag.Parse()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	runner := roles.NewRunner(
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

	var packageConfig iconfig.PackageConfiguration

	exposedResources := []exposedResource{}
	resources := []resource{
		{
			name: iconfig.ConfigName,
			path: filepath.Join("out", "package", "drafter.drftconfig"),
		},
		{
			name: iconfig.DiskName,
			path: filepath.Join("out", "package", "drafter.drftdisk"),
		},
		{
			name: iconfig.InitramfsName,
			path: filepath.Join("out", "package", "drafter.drftinitramfs"),
		},
		{
			name: iconfig.KernelName,
			path: filepath.Join("out", "package", "drafter.drftkernel"),
		},
		{
			name: iconfig.MemoryName,
			path: filepath.Join("out", "package", "drafter.drftmemory"),
		},
		{
			name: iconfig.StateName,
			path: filepath.Join("out", "package", "drafter.drftstate"),
		},
	}
	for _, res := range resources {
		if res.name == iconfig.StateName {
			stateFile, err := os.OpenFile(res.path, os.O_APPEND|os.O_WRONLY, os.ModePerm)
			if err != nil {
				panic(err)
			}
			defer stateFile.Close()

			if _, err := stateFile.Write(make([]byte, blockSize*10)); err != nil { // Add some additional blocks in case the state gets larger;
				panic(err)
			}

			if err := stateFile.Close(); err != nil {
				panic(err)
			}
		}

		stat, err := os.Stat(res.path)
		if err != nil {
			panic(err)
		}

		src, exp, err := device.NewDevice(&config.DeviceSchema{
			Name:      res.name,
			Size:      fmt.Sprintf("%v", stat.Size()),
			System:    "file",
			BlockSize: blockSize,
			Expose:    true,
			Location:  res.path,
		})
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

		if err := unix.Mknod(filepath.Join(vmPath, res.name), unix.S_IFBLK|0666, dev); err != nil {
			panic(err)
		}

		metrics := modules.NewMetrics(src)
		dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, blockSize)
		monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, blockSize, 10*time.Second)

		storage := modules.NewLockable(monitor)
		defer storage.Unlock()

		exp.SetProvider(storage)

		totalBlocks := (int(storage.Size()) + blockSize - 1) / blockSize

		orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
		orderer.AddAll()

		exposedResources = append(exposedResources, exposedResource{
			resource:    res,
			storage:     storage,
			orderer:     orderer,
			totalBlocks: totalBlocks,
			dirtyRemote: dirtyRemote,
		})
	}

	log.Println("Resuming VM")

	if err := runner.Resume(ctx, *resumeTimeout, packageConfig.AgentVSockPort); err != nil {
		panic(err)
	}

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		if err := runner.Suspend(ctx, *resumeTimeout); err != nil {
			panic(err)
		}

		if err := runner.Close(); err != nil {
			panic(err)
		}

		os.Exit(0)
	}()

	lis, err := net.Listen("tcp", ":1337")
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

	suspendedWg.Add(1)
	go func() {
		suspendWg.Wait()

		log.Println("Suspending VM")

		if err := runner.Suspend(ctx, *resumeTimeout); err != nil {
			panic(err)
		}

		suspendedWg.Done()
	}()

	var completedWg sync.WaitGroup
	completedWg.Add(len(exposedResources))

	for i, eres := range exposedResources {
		go func(i int, eres exposedResource) {
			defer completedWg.Done()

			dst := protocol.NewToProtocol(eres.storage.Size(), uint32(i), pro)
			dst.SendDevInfo(eres.resource.name, blockSize)

			go func() {
				if err := dst.HandleNeedAt(func(offset int64, length int32) {
					// Prioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(eres.storage.Size()) {
						endOffset = uint64(eres.storage.Size())
					}

					startBlock := int(offset / int64(blockSize))
					endBlock := int((endOffset-1)/uint64(blockSize)) + 1
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

			cfg := migrator.NewMigratorConfig().WithBlockSize(blockSize)
			cfg.LockerHandler = func() {
				if err := dst.SendEvent(protocol.EventPreLock); err != nil {
					panic(err)
				}

				eres.storage.Lock()

				if err := dst.SendEvent(protocol.EventPostLock); err != nil {
					panic(err)
				}
			}
			cfg.UnlockerHandler = func() {
				if err := dst.SendEvent(protocol.EventPreUnlock); err != nil {
					panic(err)
				}

				eres.storage.Unlock()

				if err := dst.SendEvent(protocol.EventPostUnlock); err != nil {
					panic(err)
				}
			}
			cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
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

			suspendVM := false
			suspendedVM := false

			var backgroundMigrationInProgress sync.WaitGroup

			for {
				if suspendVM {
					suspendVM = false
					suspendedVM = true

					suspendWg.Done()

					mig.Unlock()

					suspendedWg.Wait()

					log.Println("Passing authority to destination for", eres.resource.path)

					if err := dst.SendEvent(protocol.EventAssumeAuthority); err != nil {
						panic(err)
					}

					backgroundMigrationInProgress.Wait()
				}

				blocks := mig.GetLatestDirty()
				if blocks == nil && suspendedVM {
					mig.Unlock()

					break
				}

				// Below 128 MB; let's suspend the VM here and resume it over there
				if len(blocks) <= 2 && !suspendedVM {
					suspendVM = true
				}

				log.Println("Continously migrating", len(blocks), "blocks for", eres.resource.name)

				if err := dst.DirtyList(blocks); err != nil {
					panic(err)
				}

				if suspendVM {
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

			if err := dst.SendEvent(protocol.EventCompleted); err != nil {
				panic(err)
			}
		}(i, eres)
	}

	completedWg.Wait()

	log.Println("Completed migration, shutting down")
}
