package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/silo/pkg/storage/blocks"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
)

const (
	blockSize = 1024 * 64
)

type resource struct {
	path        string
	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resources := []resource{}
	paths := []string{
		filepath.Join("out", "package", "drafter.drftconfig"),
		filepath.Join("out", "package", "drafter.drftdisk"),
		filepath.Join("out", "package", "drafter.drftinitramfs"),
		filepath.Join("out", "package", "drafter.drftkernel"),
		filepath.Join("out", "package", "drafter.drftmemory"),
		filepath.Join("out", "package", "drafter.drftstate"),
	}
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			panic(err)
		}

		src, exp, err := device.NewDevice(&config.DeviceSchema{
			Name:      path,
			Size:      fmt.Sprintf("%v", stat.Size()),
			System:    "file",
			BlockSize: blockSize,
			Expose:    true,
			Location:  path,
		})
		if err != nil {
			panic(err)
		}
		defer src.Close()
		defer exp.Shutdown()

		log.Println("Exposed", path, "on", exp.Device())

		metrics := modules.NewMetrics(src)
		dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, blockSize)
		monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, blockSize, 10*time.Second)

		storage := modules.NewLockable(monitor)
		defer storage.Unlock()

		exp.SetProvider(storage)

		totalBlocks := (int(storage.Size()) + blockSize - 1) / blockSize

		orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
		orderer.AddAll()

		resources = append(resources, resource{
			path:        path,
			storage:     storage,
			orderer:     orderer,
			totalBlocks: totalBlocks,
			dirtyRemote: dirtyRemote,
		})
	}

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

	for i, resource := range resources {
		dst := protocol.NewToProtocol(resource.storage.Size(), uint32(i), pro)
		dst.SendDevInfo(resource.path, blockSize)

		go func() {
			if err := dst.HandleNeedAt(func(offset int64, length int32) {
				// Prioritize blocks
				endOffset := uint64(offset + int64(length))
				if endOffset > uint64(resource.storage.Size()) {
					endOffset = uint64(resource.storage.Size())
				}

				startBlock := int(offset / int64(blockSize))
				endBlock := int((endOffset-1)/uint64(blockSize)) + 1
				for b := startBlock; b < endBlock; b++ {
					resource.orderer.PrioritiseBlock(b)
				}
			}); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleDontNeedAt(func(offset int64, length int32) {
				// Deprioritize blocks
				endOffset := uint64(offset + int64(length))
				if endOffset > uint64(resource.storage.Size()) {
					endOffset = uint64(resource.storage.Size())
				}

				startBlock := int(offset / int64(resource.storage.Size()))
				endBlock := int((endOffset-1)/uint64(resource.storage.Size())) + 1
				for b := startBlock; b < endBlock; b++ {
					resource.orderer.Remove(b)
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

			resource.storage.Lock()

			if err := dst.SendEvent(protocol.EventPostLock); err != nil {
				panic(err)
			}
		}
		cfg.UnlockerHandler = func() {
			if err := dst.SendEvent(protocol.EventPreUnlock); err != nil {
				panic(err)
			}

			resource.storage.Unlock()

			if err := dst.SendEvent(protocol.EventPostUnlock); err != nil {
				panic(err)
			}
		}
		cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
			// log.Printf("%v/%v", p.ReadyBlocks, p.TotalBlocks)
		}

		mig, err := migrator.NewMigrator(resource.dirtyRemote, dst, resource.orderer, cfg)
		if err != nil {
			panic(err)
		}

		log.Println("Migrating", resource.totalBlocks, "blocks")

		if err := mig.Migrate(resource.totalBlocks); err != nil {
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
				log.Println("Suspending VM")

				suspendVM = false
				suspendedVM = true

				log.Println("Asking remote VM to resume")

				if err := dst.SendEvent(protocol.EventResume); err != nil {
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

			log.Println("Continously migrating", len(blocks), "blocks")

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
	}

	log.Println("Completed migration, shutting down")
}
