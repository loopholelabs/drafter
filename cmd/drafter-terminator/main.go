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

			// TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
			if conn != nil {
				if err := conn.Close(); err != nil && !utils.IsClosedErr(err) {
					panic(err)
				}
			}
		}()

		if err := roles.Terminate(
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
		); err != nil && !utils.IsClosedErr(err) { // TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
			panic(err)
		}

		log.Println("Shutting down")
	}
}
