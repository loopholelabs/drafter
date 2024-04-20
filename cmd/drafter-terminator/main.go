package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := net.Dial("tcp", *raddr)
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

					st, err := sources.NewFileStorageCreate(path, int64(di.Size))
					if err != nil {
						panic(err)
					}

					var remote *waitingcache.WaitingCacheRemote
					local, remote = waitingcache.NewWaitingCache(st, int(di.BlockSize))
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

			go func() {
				if err := from.HandleSend(ctx); err != nil {
					panic(err)
				}
			}()

			go func() {
				if err := from.HandleReadAt(); err != nil {
					panic(err)
				}
			}()

			go func() {
				if err := from.HandleWriteAt(); err != nil {
					panic(err)
				}
			}()

			go func() {
				if err := from.HandleDevInfo(); err != nil {
					panic(err)
				}
			}()

			go func() {
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
				}); err != nil {
					panic(err)
				}
			}()

			go func() {
				if err := from.HandleDirtyList(func(blocks []uint) {
					if local != nil {
						local.DirtyBlocks(blocks)
					}
				}); err != nil {
					panic(err)
				}
			}()
		})

	if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
		panic(err)
	}

	log.Println("Completed all migrations, shutting down")
}
