package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	defaultDevices, err := json.Marshal([]roles.RegistryDevice{
		{
			Name:      roles.StateName,
			Input:     filepath.Join("out", "package", "state.bin"),
			BlockSize: 1024 * 64,
		},
		{
			Name:      roles.MemoryName,
			Input:     filepath.Join("out", "package", "memory.bin"),
			BlockSize: 1024 * 64,
		},

		{
			Name:      roles.KernelName,
			Input:     filepath.Join("out", "package", "vmlinux"),
			BlockSize: 1024 * 64,
		},
		{
			Name:      roles.DiskName,
			Input:     filepath.Join("out", "package", "rootfs.ext4"),
			BlockSize: 1024 * 64,
		},

		{
			Name:      roles.ConfigName,
			Input:     filepath.Join("out", "package", "config.json"),
			BlockSize: 1024 * 64,
		},

		{
			Name:      "oci",
			Input:     filepath.Join("out", "blueprint", "oci.ext4"),
			BlockSize: 1024 * 64,
		},
	})
	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	laddr := flag.String("laddr", ":1600", "Address to listen on")

	concurrency := flag.Int("concurrency", 4096, "Amount of concurrent workers to use in migrations")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []roles.RegistryDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
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
