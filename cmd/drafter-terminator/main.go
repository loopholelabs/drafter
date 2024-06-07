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
			Output: filepath.Join("out", "package", "drafter.drftstate"),
		},
		{
			Name:   roles.MemoryName,
			Output: filepath.Join("out", "package", "drafter.drftmemory"),
		},

		{
			Name:   roles.InitramfsName,
			Output: filepath.Join("out", "package", "drafter.drftinitramfs"),
		},
		{
			Name:   roles.KernelName,
			Output: filepath.Join("out", "package", "drafter.drftkernel"),
		},
		{
			Name:   roles.DiskName,
			Output: filepath.Join("out", "package", "drafter.drftdisk"),
		},

		{
			Name:   roles.ConfigName,
			Output: filepath.Join("out", "package", "drafter.drftconfig"),
		},

		{
			Name:   "oci",
			Output: filepath.Join("out", "package", "drafter.drftoci"),
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
	}()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", *raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

	if err := roles.Terminate(
		ctx,

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
