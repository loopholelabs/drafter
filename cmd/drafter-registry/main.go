package main

import (
	"flag"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	v1 "github.com/loopholelabs/drafter/pkg/api/proto/migration/v1"
	backend "github.com/loopholelabs/drafter/pkg/backends"
	iservices "github.com/loopholelabs/drafter/pkg/services"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/r3map/pkg/services"
	"google.golang.org/grpc"
)

type resources struct {
	laddr  string
	path   string
	size   int64
	lis    net.Listener
	server *grpc.Server
}

func main() {
	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	stateLaddr := flag.String("state-laddr", ":1600", "Listen address for state")
	memoryLaddr := flag.String("memory-laddr", ":1601", "Listen address for memory")
	initramfsLaddr := flag.String("initramfs-laddr", ":1602", "Listen address for initramfs")
	kernelLaddr := flag.String("kernel-laddr", ":1603", "Listen address for kernel")
	diskLaddr := flag.String("disk-laddr", ":1604", "Listen address for disk")
	configLaddr := flag.String("config-laddr", ":1605", "Listen address for config")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	resources := []*resources{
		{
			laddr: *initramfsLaddr,
			path:  *initramfsPath,
		},
		{
			laddr: *kernelLaddr,
			path:  *kernelPath,
		},
		{
			laddr: *diskLaddr,
			path:  *diskPath,
		},

		{
			laddr: *stateLaddr,
			path:  *statePath,
		},
		{
			laddr: *memoryLaddr,
			path:  *memoryPath,
		},

		{
			laddr: *configLaddr,
			path:  *configPath,
		},
	}
	for _, resource := range resources {
		r := resource

		f, err := os.Open(resource.path)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			panic(err)
		}

		r.size = stat.Size()

		b := backend.NewReadOnlyBackend(f, r.size)

		svc := iservices.NewSeederWithMetaService(
			services.NewSeederService(
				b,
				*verbose,
				func() error {
					return nil
				},
				func() ([]int64, error) {
					return []int64{}, nil
				},
				func() error {
					return nil
				},
				services.MaxChunkSize,
			),
			b,
			*verbose,
		)

		server := grpc.NewServer()
		r.server = server

		v1.RegisterSeederWithMetaServer(server, iservices.NewSeederWithMetaServiceGrpc(svc))

		lis, err := net.Listen("tcp", r.laddr)
		if err != nil {
			panic(err)
		}
		defer lis.Close()
		r.lis = lis
	}

	var wg sync.WaitGroup
	for _, rsc := range resources {
		r := rsc

		log.Printf("Seeding %v (%v bytes) on %v", r.path, r.size, r.laddr)

		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := r.server.Serve(r.lis); err != nil {
				if !utils.IsClosedErr(err) {
					panic(err)
				}

				return
			}
		}()
	}

	wg.Wait()
}
