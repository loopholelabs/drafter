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
	"path/filepath"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type CustomEventType byte

const (
	EventCustomPassAuthority  = CustomEventType(0)
	EventCustomAllDevicesSent = CustomEventType(1)
)

var (
	errUnknownDeviceName = errors.New("unknown device name")
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

	errs := []error{}
	var errsLock sync.Mutex

	defer func() {
		errsLock.Lock()
		defer errsLock.Unlock()

		if len(errs) > 0 {
			log.Fatal(errs)
		}
	}()

	defer func() {
		if err := recover(); err != nil {
			errsLock.Lock()
			defer errsLock.Unlock()

			errs = append(errs, fmt.Errorf("%v", err))
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		conn net.Conn
		err  error
	)
	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				errs = append(errs, fmt.Errorf("%v", err))

				cancel()

				// TODO: Make `func (p *protocol.ProtocolRW) Handle() error` return if context is cancelled, then remove this workaround
				if conn != nil {
					if err := conn.Close(); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}
	}

	conn, err = net.Dial("tcp", *raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

	pro := protocol.NewProtocolRW(
		ctx,
		[]io.Reader{conn},
		[]io.Writer{conn},
		func(p protocol.Protocol, u uint32) {
			var (
				from  *protocol.FromProtocol
				local *waitingcache.WaitingCacheLocal
			)
			from = protocol.NewFromProtocol(
				u,
				func(di *protocol.DevInfo) storage.StorageProvider {
					defer handleGoroutinePanic()()

					var (
						path = ""
					)
					switch di.Name {
					case config.ConfigName:
						path = *configPath

					case config.DiskName:
						path = *diskPath

					case config.InitramfsName:
						path = *initramfsPath

					case config.KernelName:
						path = *kernelPath

					case config.MemoryName:
						path = *memoryPath

					case config.StateName:
						path = *statePath
					}

					if strings.TrimSpace(path) == "" {
						panic(errUnknownDeviceName)
					}

					log.Println("Received device", u, "with name", di.Name)

					if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
						panic(err)
					}

					storage, err := sources.NewFileStorageCreate(path, int64(di.Size))
					if err != nil {
						panic(err)
					}

					var remote *waitingcache.WaitingCacheRemote
					local, remote = waitingcache.NewWaitingCache(storage, int(di.BlockSize))
					local.NeedAt = func(offset int64, length int32) {
						if err := from.NeedAt(offset, length); err != nil {
							panic(err)
						}
					}
					local.DontNeedAt = func(offset int64, length int32) {
						if err := from.DontNeedAt(offset, length); err != nil {
							panic(err)
						}
					}

					return remote
				},
				p,
			)

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleSend(ctx); err != nil && !errors.Is(err, context.Canceled) {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleReadAt(); err != nil && !errors.Is(err, context.Canceled) {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleWriteAt(); err != nil && !errors.Is(err, context.Canceled) {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleDevInfo(); err != nil {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleEvent(func(e *protocol.Event) {
					switch e.Type {
					case protocol.EventCustom:
						switch e.CustomType {
						case byte(EventCustomPassAuthority):
							log.Println("Received authority for device", u)

						case byte(EventCustomAllDevicesSent):
							log.Println("Received all devices")
						}

					case protocol.EventCompleted:
						log.Println("Completed migration of device", u)
					}
				}); err != nil && !errors.Is(err, context.Canceled) {
					panic(err)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer handleGoroutinePanic()()

				if err := from.HandleDirtyList(func(blocks []uint) {
					if local != nil {
						local.DirtyBlocks(blocks)
					}
				}); err != nil && !errors.Is(err, context.Canceled) {
					panic(err)
				}
			}()
		})

	if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
		panic(err)
	}

	log.Println("Completed all migrations, shutting down")
}
