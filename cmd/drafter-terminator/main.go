package main

import (
	"context"
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
	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := net.Dial("tcp", *raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

	errs := []error{}

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()

		// TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
		if conn != nil {
			if err := conn.Close(); err != nil && !utils.IsClosedErr(err) {
				errs = append(errs, err)
			}
		}
	}()

	errs = append(
		errs,
		roles.Terminate(
			ctx,
			conn,

			*statePath,
			*memoryPath,
			*initramfsPath,
			*kernelPath,
			*diskPath,
			*configPath,

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
					log.Println("Completed all migrations")
				},
			},
		)...,
	)

	for _, err := range errs {
		// TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
		if err != nil && !utils.IsClosedErr(err) {
			select {
			case <-ctx.Done():
				return

			default:
				panic(err)
			}
		}
	}

	log.Println("Shutting down")
}
