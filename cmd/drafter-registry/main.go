package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
)

var (
	errInterrupted = errors.New("interrupted")
)

func main() {
	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	stateBlockSize := flag.Uint("state-block-size", 1024*64, "State block size")
	memoryBlockSize := flag.Uint("memory-block-size", 1024*64, "Memory block size")
	initramfsBlockSize := flag.Uint("initramfs-block-size", 1024*64, "initramfs block size")
	kernelBlockSize := flag.Uint("kernel-block-size", 1024*64, "Kernel block size")
	diskBlockSize := flag.Uint("disk-block-size", 1024*64, "Disk block size")
	configBlockSize := flag.Uint("config-block-size", 1024*64, "Config block size")

	laddr := flag.String("laddr", ":1600", "Address to listen on")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

	flag.Parse()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(errInterrupted)

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Serving on", lis.Addr())

	errs := []error{}

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel(errInterrupted)

		if lis != nil {
			if err := lis.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}()

	defer log.Println("Shutting down")

	for {
		func() {
			conn, err := lis.Accept()
			if err != nil {
				log.Println("could not accept connection, continuing:", err)

				return
			}

			log.Println("Migrating tcmd/drafter-terminatoro", conn.RemoteAddr())

			go func() {
				defer func() {
					_ = conn.Close()

					if err := recover(); err != nil {
						log.Printf("Registry client disconnected with error: %v", err)
					}
				}()

				devices, defers, errs := roles.OpenDevices(
					*statePath,
					*memoryPath,
					*initramfsPath,
					*kernelPath,
					*diskPath,
					*configPath,

					uint32(*stateBlockSize),
					uint32(*memoryBlockSize),
					uint32(*initramfsBlockSize),
					uint32(*kernelBlockSize),
					uint32(*diskBlockSize),
					uint32(*configBlockSize),

					roles.OpenDevicesHooks{
						OnOpenDevice: func(deviceID uint32, name string) {
							log.Println("Opening device", deviceID, "with name", name)
						},
					},
				)

				for _, err := range errs {
					if err != nil && !(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errInterrupted)) {
						panic(err)
					}
				}

				for _, deferFunc := range defers {
					defer deferFunc()
				}

				{
					var completedWg sync.WaitGroup
					completedWg.Add(len(devices))

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

					for u, eres := range devices {
						go func(u int, eres roles.Device) {
							defer completedWg.Done()

							to := protocol.NewToProtocol(eres.Storage.Size(), uint32(u), pro)

							log.Println("Sending device", u)

							if err := to.SendDevInfo(eres.Prev.Name, eres.Prev.BlockSize); err != nil {
								panic(err)
							}
							devicesLeftToSend.Add(1)

							if devicesLeftToSend.Load() >= int32(len(devices)) {
								go func() {
									if err := to.SendEvent(&protocol.Event{
										Type:       protocol.EventCustom,
										CustomType: byte(roles.EventCustomAllDevicesSent),
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
									if endOffset > uint64(eres.Storage.Size()) {
										endOffset = uint64(eres.Storage.Size())
									}

									startBlock := int(offset / int64(eres.Prev.BlockSize))
									endBlock := int((endOffset-1)/uint64(eres.Prev.BlockSize)) + 1
									for b := startBlock; b < endBlock; b++ {
										eres.Orderer.PrioritiseBlock(b)
									}
								}); err != nil {
									panic(err)
								}
							}()

							go func() {
								if err := to.HandleDontNeedAt(func(offset int64, length int32) {
									// Deprioritize blocks
									endOffset := uint64(offset + int64(length))
									if endOffset > uint64(eres.Storage.Size()) {
										endOffset = uint64(eres.Storage.Size())
									}

									startBlock := int(offset / int64(eres.Storage.Size()))
									endBlock := int((endOffset-1)/uint64(eres.Storage.Size())) + 1
									for b := startBlock; b < endBlock; b++ {
										eres.Orderer.Remove(b)
									}
								}); err != nil {
									panic(err)
								}
							}()

							cfg := migrator.NewMigratorConfig().WithBlockSize(int(eres.Prev.BlockSize))
							cfg.Concurrency = map[int]int{
								storage.BlockTypeAny:      *concurrency,
								storage.BlockTypeStandard: *concurrency,
								storage.BlockTypeDirty:    *concurrency,
								storage.BlockTypePriority: *concurrency,
							}
							cfg.ProgressHandler = func(p *migrator.MigrationProgress) {
								log.Printf("Migrated %v/%v blocks for device %v", p.ReadyBlocks, p.TotalBlocks, u)
							}

							mig, err := migrator.NewMigrator(eres.DirtyRemote, to, eres.Orderer, cfg)
							if err != nil {
								panic(err)
							}

							log.Println("Migrating", eres.TotalBlocks, "blocks for device", u)

							go func() {
								if err := to.SendEvent(&protocol.Event{
									Type:       protocol.EventCustom,
									CustomType: byte(roles.EventCustomPassAuthority),
								}); err != nil {
									panic(err)
								}

								log.Println("Transferred authority for device", u)
							}()

							if err := mig.Migrate(eres.TotalBlocks); err != nil {
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
				}
			}()
		}()
	}
}
