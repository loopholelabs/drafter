package main

import (
	"context"
	"io"
	"log"
	"net"
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

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src, exp, err := device.NewDevice(&config.DeviceSchema{
		Name:      "test",
		Size:      "1G",
		System:    "file",
		BlockSize: blockSize,
		Expose:    true,
		Location:  filepath.Join("out", "test.bin"),
	})
	if err != nil {
		panic(err)
	}
	defer src.Close()
	defer exp.Shutdown()

	log.Println("Exposed on", exp.Device())

	metrics := modules.NewMetrics(src)
	dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(metrics, blockSize)
	monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, blockSize, 10*time.Second)

	storage := modules.NewLockable(monitor)
	defer storage.Unlock()

	exp.SetProvider(storage)

	totalBlocks := (int(storage.Size()) + blockSize - 1) / blockSize

	orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
	orderer.AddAll()

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

	dst := protocol.NewToProtocol(storage.Size(), 0, pro)
	dst.SendDevInfo("test", blockSize)

	go func() {
		if err := dst.HandleNeedAt(func(offset int64, length int32) {
			// Prioritize blocks
			endOffset := uint64(offset + int64(length))
			if endOffset > uint64(storage.Size()) {
				endOffset = uint64(storage.Size())
			}

			startBlock := int(offset / int64(blockSize))
			endBlock := int((endOffset-1)/uint64(blockSize)) + 1
			for b := startBlock; b < endBlock; b++ {
				orderer.PrioritiseBlock(b)
			}
		}); err != nil {
			panic(err)
		}
	}()

	go func() {
		if err := dst.HandleDontNeedAt(func(offset int64, length int32) {
			// Deprioritize blocks
			endOffset := uint64(offset + int64(length))
			if endOffset > uint64(storage.Size()) {
				endOffset = uint64(storage.Size())
			}

			startBlock := int(offset / int64(storage.Size()))
			endBlock := int((endOffset-1)/uint64(storage.Size())) + 1
			for b := startBlock; b < endBlock; b++ {
				orderer.Remove(b)
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

		storage.Lock()

		if err := dst.SendEvent(protocol.EventPostLock); err != nil {
			panic(err)
		}
	}
	cfg.UnlockerHandler = func() {
		if err := dst.SendEvent(protocol.EventPreUnlock); err != nil {
			panic(err)
		}

		storage.Unlock()

		if err := dst.SendEvent(protocol.EventPostUnlock); err != nil {
			panic(err)
		}
	}
	cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
		// log.Printf("%v/%v", p.ReadyBlocks, p.TotalBlocks)
	}

	mig, err := migrator.NewMigrator(dirtyRemote, dst, orderer, cfg)
	if err != nil {
		panic(err)
	}

	log.Println("Migrating", totalBlocks, "blocks")

	if err := mig.Migrate(totalBlocks); err != nil {
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

		log.Println("Migrating", len(blocks), "blocks")

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

	log.Println("Completed migration, shutting down")
}
