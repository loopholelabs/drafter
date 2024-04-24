package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
)

var (
	errFinished = errors.New("finished")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Serving on", lis.Addr())

	{
		var errsLock sync.Mutex
		var errs error

		defer func() {
			if errs != nil {
				panic(errs)
			}
		}()

		var wg sync.WaitGroup
		defer wg.Wait()

		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(errFinished)

		handleGoroutinePanic := func() func() {
			return func() {
				if err := recover(); err != nil {
					errsLock.Lock()
					defer errsLock.Unlock()

					var e error
					if v, ok := err.(error); ok {
						e = v
					} else {
						e = fmt.Errorf("%v", err)
					}

					if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
						errs = errors.Join(errs, e)
					}

					cancel(errFinished)
				}
			}
		}

		defer handleGoroutinePanic()()

		go func() {
			done := make(chan os.Signal, 1)
			signal.Notify(done, os.Interrupt)

			<-done

			wg.Add(1) // We only register this here since we still want to be able to exit without manually interrupting
			defer wg.Done()

			defer handleGoroutinePanic()()

			log.Println("Exiting gracefully")

			cancel(errFinished)

			if lis != nil {
				if err := lis.Close(); err != nil {
					panic(err)
				}
			}
		}()

	l:
		for {
			conn, err := lis.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					break l

				default:
					panic(err)
				}
			}

			log.Println("Migrating to", conn.RemoteAddr())

			go func() {
				defer conn.Close() // TODO: Keep this even after the Goroutine leaks are solved since the conn handler will no longer close the connections
				defer func() {
					if err := recover(); err != nil {
						var e error
						if v, ok := err.(error); ok {
							e = v
						} else {
							e = fmt.Errorf("%v", err)
						}

						// TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
						if !utils.IsClosedErr(e) {
							log.Printf("Registry client disconnected with error: %v", e)
						}
					}
				}()

				devices, defers, err := roles.OpenDevices(
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
						OnDeviceOpened: func(deviceID uint32, name string) {
							log.Println("Opened device", deviceID, "with name", name)
						},
					},
				)
				if err != nil {
					panic(err)
				}

				for _, deferFunc := range defers {
					defer deferFunc()
				}

				if err := roles.MigrateDevices(
					ctx,
					conn,

					devices,
					*concurrency,

					[]io.Reader{conn},
					[]io.Writer{conn},

					roles.MigrateDevicesHooks{
						OnDeviceSent: func(deviceID uint32) {
							log.Println("Sent device", deviceID)
						},
						OnDeviceAuthoritySent: func(deviceID uint32) {
							log.Println("Sent authority for device", deviceID)
						},
						OnDeviceMigrationProgress: func(deviceID uint32, ready, total int) {
							log.Println("Migrated", ready, "of", total, "blocks for device", deviceID)
						},
						OnDeviceMigrationCompleted: func(deviceID uint32) {
							log.Println("Completed migration of device", deviceID)
						},

						OnAllDevicesSent: func() {
							log.Println("Sent all devices")
						},
						OnAllMigrationsCompleted: func() {
							log.Println("Completed all migrations")
						},
					},
				); err != nil {
					panic(err)
				}
			}()
		}

		log.Println("Shutting down")
	}
}
