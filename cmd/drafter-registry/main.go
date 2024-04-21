package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	iconfig "github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

type CustomEventType byte

const (
	EventCustomPassAuthority  = CustomEventType(0)
	EventCustomAllDevicesSent = CustomEventType(1)
)

type resource struct {
	name      string
	blockSize uint32
	base      string
}

type exposedResource struct {
	resource resource

	size        uint64
	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

func main() {
	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	laddr := flag.String("laddr", ":1600", "Address to listen on")
	blockSize := flag.Uint("block-size", 1024*64, "Block size to use (serve use only)")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resources := []resource{
		{
			name:      iconfig.ConfigName,
			blockSize: uint32(*blockSize),
			base:      *configPath,
		},
		{
			name:      iconfig.DiskName,
			blockSize: uint32(*blockSize),
			base:      *diskPath,
		},
		{
			name:      iconfig.InitramfsName,
			blockSize: uint32(*blockSize),
			base:      *initramfsPath,
		},
		{
			name:      iconfig.KernelName,
			blockSize: uint32(*blockSize),
			base:      *kernelPath,
		},
		{
			name:      iconfig.MemoryName,
			blockSize: uint32(*blockSize),
			base:      *memoryPath,
		},
		{
			name:      iconfig.StateName,
			blockSize: uint32(*blockSize),
			base:      *statePath,
		},
	}

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Serving on", lis.Addr())

	for {
		func() {
			conn, err := lis.Accept()
			if err != nil {
				log.Println("could not accept connection, continuing:", err)

				return
			}

			log.Println("Migrating to", conn.RemoteAddr())

			go func() {
				defer func() {
					_ = conn.Close()

					if err := recover(); err != nil && !utils.IsClosedErr(err.(error)) {
						log.Printf("Control plane client disconnected with error: %v", err)
					}
				}()

				exposedResources := []exposedResource{}
				for u, res := range resources {
					log.Println("Opening device", u, "with name", res.name)

					eres := exposedResource{
						resource: res,
					}

					stat, err := os.Stat(res.base)
					if err != nil {
						panic(err)
					}
					eres.size = uint64(stat.Size())

					src, _, err := device.NewDevice(&config.DeviceSchema{
						Name:      res.name,
						System:    "file",
						Location:  res.base,
						Size:      fmt.Sprintf("%v", eres.size),
						BlockSize: fmt.Sprintf("%v", res.blockSize),
						Expose:    false,
					})
					if err != nil {
						panic(err)
					}
					defer src.Close()

					metrics := modules.NewMetrics(src)
					dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, int(res.blockSize))
					eres.dirtyRemote = dirtyRemote
					monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(res.blockSize), 10*time.Second)

					storage := modules.NewLockable(monitor)
					eres.storage = storage
					defer storage.Unlock()

					totalBlocks := (int(storage.Size()) + int(res.blockSize) - 1) / int(res.blockSize)
					eres.totalBlocks = totalBlocks

					orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
					eres.orderer = orderer
					orderer.AddAll()

					exposedResources = append(exposedResources, eres)
				}

				var completedWg sync.WaitGroup
				completedWg.Add(len(exposedResources))

				var devicesLeftToSend atomic.Int32

				pro := protocol.NewProtocolRW(
					ctx,
					[]io.Reader{conn},
					[]io.Writer{conn},
					nil,
				)

				go func() {
					if err := pro.Handle(); err != nil {
						panic(err)
					}
				}()

				for u, eres := range exposedResources {
					go func(u int, eres exposedResource) {
						defer completedWg.Done()

						to := protocol.NewToProtocol(eres.storage.Size(), uint32(u), pro)

						log.Println("Sending device", u)

						if err := to.SendDevInfo(eres.resource.name, eres.resource.blockSize); err != nil {
							panic(err)
						}
						devicesLeftToSend.Add(1)

						if devicesLeftToSend.Load() >= int32(len(exposedResources)) {
							go func() {
								if err := to.SendEvent(&protocol.Event{
									Type:       protocol.EventCustom,
									CustomType: byte(EventCustomAllDevicesSent),
								}); err != nil {
									panic(err)
								}

								log.Println("Sent all devices")
							}()
						}

						go func() {
							if err := to.HandleNeedAt(func(offset int64, length int32) {
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
							if err := to.HandleDontNeedAt(func(offset int64, length int32) {
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
						cfg.LockerHandler = func() {
							if err := to.SendEvent(&protocol.Event{
								Type: protocol.EventPreLock,
							}); err != nil {
								panic(err)
							}

							eres.storage.Lock()

							if err := to.SendEvent(&protocol.Event{
								Type: protocol.EventPostLock,
							}); err != nil {
								panic(err)
							}

						}
						cfg.UnlockerHandler = func() {
							if err := to.SendEvent(&protocol.Event{
								Type: protocol.EventPreUnlock,
							}); err != nil {
								panic(err)
							}

							eres.storage.Unlock()

							if err := to.SendEvent(&protocol.Event{
								Type: protocol.EventPostUnlock,
							}); err != nil {
								panic(err)
							}
						}
						cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
							// log.Printf("%v/%v", p.ReadyBlocks, p.TotalBlocks)
						}

						mig, err := migrator.NewMigrator(eres.dirtyRemote, to, eres.orderer, cfg)
						if err != nil {
							panic(err)
						}

						log.Println("Migrating", eres.totalBlocks, "blocks for device", u)

						go func() {
							if err := to.SendEvent(&protocol.Event{
								Type:       protocol.EventCustom,
								CustomType: byte(EventCustomPassAuthority),
							}); err != nil {
								panic(err)
							}

							log.Println("Transferred authority for device", u)
						}()

						if err := mig.Migrate(eres.totalBlocks); err != nil {
							panic(err)
						}

						if err := mig.WaitForCompletion(); err != nil {
							panic(err)
						}

						if err := to.SendEvent(&protocol.Event{
							Type: protocol.EventCompleted,
						}); err != nil {
							panic(err)
						}

						log.Println("Completed migration of device", u)
					}(u, eres)
				}

				completedWg.Wait()

				log.Println("Completed all migrations, closing connection")
			}()
		}()
	}
}
