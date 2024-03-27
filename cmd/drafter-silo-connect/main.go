package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"sync"

	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/expose"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := net.Dial("tcp", "localhost:1337")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Migrating from", conn.RemoteAddr())

	var (
		completedWg sync.WaitGroup
		resumedWg   sync.WaitGroup
	)
	completedWg.Add(1)
	resumedWg.Add(1)

	firstMigration := true

	var exp storage.ExposedStorage
	pro := protocol.NewProtocolRW(ctx, []io.Reader{conn}, []io.Writer{conn}, func(p protocol.Protocol, u uint32) {
		if !firstMigration {
			completedWg.Add(1)
			resumedWg.Add(1)
		}
		firstMigration = false

		var (
			dst   *protocol.FromProtocol
			local *waitingcache.WaitingCacheLocal
		)
		dst = protocol.NewFromProtocol(
			u,
			func(di *protocol.DevInfo) storage.StorageProvider {
				shardSize := di.Size
				if di.Size > 64*1024 {
					shardSize = di.Size / 1024
				}

				shards, err := modules.NewShardedStorage(
					int(di.Size),
					int(shardSize),
					func(index, size int) (storage.StorageProvider, error) {
						return sources.NewFileStorageCreate(filepath.Join("out", fmt.Sprintf("test-%v.bin", index)), int64(size))
					},
				)
				if err != nil {
					panic(err)
				}

				var remote *waitingcache.WaitingCacheRemote
				local, remote = waitingcache.NewWaitingCache(shards, int(di.BlockSize))
				local.NeedAt = func(offset int64, length int32) {
					dst.NeedAt(offset, length)
				}
				local.DontNeedAt = func(offset int64, length int32) {
					dst.DontNeedAt(offset, length)
				}

				exp = expose.NewExposedStorageNBDNL(local, 1, 0, local.Size(), 4096, true)

				if err := exp.Init(); err != nil {
					panic(err)
				}

				log.Println("Exposed on", exp.Device())

				return remote
			},
			p,
		)

		go func() {
			if err := dst.HandleSend(ctx); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleReadAt(); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleWriteAt(); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleDevInfo(); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleEvent(func(et protocol.EventType) {
				switch et {
				case protocol.EventCompleted:
					completedWg.Done()

				case protocol.EventResume:
					resumedWg.Done()
				}
			}); err != nil {
				panic(err)
			}
		}()

		go func() {
			if err := dst.HandleDirtyList(func(blocks []uint) {
				log.Println("Migrating", blocks, "blocks")

				if local != nil {
					local.DirtyBlocks(blocks)
				}
			}); err != nil {
				panic(err)
			}
		}()
	})
	defer func() {
		if exp != nil {
			_ = exp.Shutdown()
		}
	}()

	go func() {
		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}
	}()

	resumedWg.Wait()

	log.Println("Resuming VM")

	completedWg.Wait()

	log.Println("Completed migration, idling")

	select {}
}
