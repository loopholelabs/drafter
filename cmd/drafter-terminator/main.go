package main

import (
	"context"
	"encoding/json"
	"flag"
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
	defaultDevices, err := json.Marshal([]roles.TerminatorDevice{
		{
			Name:   roles.StateName,
			Output: filepath.Join("out", "package", "state.bin"),
		},
		{
			Name:   roles.MemoryName,
			Output: filepath.Join("out", "package", "memory.bin"),
		},

		{
			Name:   roles.KernelName,
			Output: filepath.Join("out", "package", "vmlinux"),
		},
		{
			Name:   roles.DiskName,
			Output: filepath.Join("out", "package", "rootfs.ext4"),
		},

		{
			Name:   roles.ConfigName,
			Output: filepath.Join("out", "package", "config.json"),
		},

		{
			Name:   "oci",
			Output: filepath.Join("out", "package", "oci.ext4"),
		},
	})
	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []roles.TerminatorDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	goroutineManager := utils.NewGoroutineManager(
		ctx,
		&errs,
		utils.GoroutineManagerHooks{},
	)
	defer goroutineManager.WaitForForegroundGoroutines()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	conn, err := (&net.Dialer{}).DialContext(goroutineManager.GetGoroutineCtx(), "tcp", *raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

	if err := roles.Terminate(
		goroutineManager.GetGoroutineCtx(),

		devices,

		[]io.Reader{conn},
		[]io.Writer{conn},

		roles.TerminateHooks{
			OnDeviceReceived: func(deviceID uint32, name string) {
				log.Println("Received device", deviceID, "with name", name)
			},
			OnDeviceAuthorityReceived: func(deviceID uint32) {
				log.Println("Received authority for device", deviceID)
			},
			OnDeviceMigrationCompleted: func(deviceID uint32) {
				log.Println("Completed migration of device", deviceID)
			},

			OnAllDevicesReceived: func() {
				log.Println("Received all devices")
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
