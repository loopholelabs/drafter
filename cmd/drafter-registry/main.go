package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
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

	stateServe := flag.Bool("state-serve", true, "Whether to serve the state")
	memoryServe := flag.Bool("memory-serve", true, "Whether to serve the memory")
	initramfsServe := flag.Bool("initramfs-serve", true, "Whether to serve the initramfs")
	kernelServe := flag.Bool("kernel-serve", true, "Whether to serve the kernel")
	diskServe := flag.Bool("disk-serve", true, "Whether to serve the disk")
	configServe := flag.Bool("config-serve", true, "Whether to serve the config")

	laddr := flag.String("laddr", ":1600", "Address to listen on")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices := []roles.Device{}

	if *stateServe {
		devices = append(devices, roles.Device{
			Name:      config.StateName,
			Base:      *statePath,
			BlockSize: uint32(*stateBlockSize),
		})
	}

	if *memoryServe {
		devices = append(devices, roles.Device{
			Name:      config.MemoryName,
			Base:      *memoryPath,
			BlockSize: uint32(*memoryBlockSize),
		})
	}

	if *initramfsServe {
		devices = append(devices, roles.Device{
			Name:      config.InitramfsName,
			Base:      *initramfsPath,
			BlockSize: uint32(*initramfsBlockSize),
		})
	}

	if *kernelServe {
		devices = append(devices, roles.Device{
			Name:      config.KernelName,
			Base:      *kernelPath,
			BlockSize: uint32(*kernelBlockSize),
		})
	}

	if *diskServe {
		devices = append(devices, roles.Device{
			Name:      config.DiskName,
			Base:      *diskPath,
			BlockSize: uint32(*diskBlockSize),
		})
	}

	if *configServe {
		devices = append(devices, roles.Device{
			Name:      config.ConfigName,
			Base:      *configPath,
			BlockSize: uint32(*configBlockSize),
		})
	}

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Serving on", lis.Addr())

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	ctx, handlePanics, _, cancel, wait, _ := utils.GetPanicHandler(
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

		defer handlePanics(true)()

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
			defer conn.Close()
			defer func() {
				if err := recover(); err != nil {
					var e error
					if v, ok := err.(error); ok {
						e = v
					} else {
						e = fmt.Errorf("%v", err)
					}

					log.Printf("Registry client disconnected with error: %v", e)
				}
			}()

			openedDevices, defers, err := roles.OpenDevices(
				devices,

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

			if err := roles.MigrateOpenedDevices(
				ctx,

				openedDevices,
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
						log.Println("Completed all device migrations")
					},
				},
			); err != nil {
				panic(err)
			}
		}()
	}

	log.Println("Shutting down")
}
