package main

import (
	"archive/tar"
	"flag"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	backend "github.com/loopholelabs/architekt/pkg/backends"
	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/roles"
	iservices "github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
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
	packagePath := flag.String("package-path", filepath.Join("out", "redis.ark"), "Path to package to serve")

	stateLaddr := flag.String("state-laddr", ":1600", "Listen address for state")
	memoryLaddr := flag.String("memory-laddr", ":1601", "Listen address for memory")
	initramfsLaddr := flag.String("initramfs-laddr", ":1602", "Listen address for initramfs")
	kernelLaddr := flag.String("kernel-laddr", ":1603", "Listen address for kernel")
	diskLaddr := flag.String("disk-laddr", ":1604", "Listen address for disk")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	packageFile, err := os.OpenFile(*packagePath, os.O_RDWR, os.ModePerm)
	if err != nil {
		panic(err)
	}
	defer packageFile.Close()

	packageArchive := tar.NewReader(packageFile)

	packageConfig, _, err := utils.ReadPackageConfigFromTar(packageArchive)
	if err != nil {
		panic(err)
	}

	if err := packageFile.Close(); err != nil {
		panic(err)
	}

	resources := []*resources{
		{
			laddr: *stateLaddr,
			path:  firecracker.StateName,
		},
		{
			laddr: *memoryLaddr,
			path:  firecracker.MemoryName,
		},
		{
			laddr: *initramfsLaddr,
			path:  roles.InitramfsName,
		},
		{
			laddr: *kernelLaddr,
			path:  roles.KernelName,
		},
		{
			laddr: *diskLaddr,
			path:  roles.DiskName,
		},
	}
	for _, resource := range resources {
		backendFile, err := os.OpenFile(*packagePath, os.O_RDWR, os.ModePerm)
		if err != nil {
			panic(err)
		}
		defer backendFile.Close()

		b := backend.NewTarBackend(backendFile, resource.path)

		if err := b.Open(); err != nil {
			panic(err)
		}

		size, err := b.Size()
		if err != nil {
			panic(err)
		}
		resource.size = size

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
			packageConfig.AgentVSockPort,
			*verbose,
		)

		server := grpc.NewServer()
		resource.server = server

		v1.RegisterSeederWithMetaServer(server, iservices.NewSeederWithMetaServiceGrpc(svc))

		lis, err := net.Listen("tcp", resource.laddr)
		if err != nil {
			panic(err)
		}
		defer lis.Close()
		resource.lis = lis
	}

	var wg sync.WaitGroup
	for _, resource := range resources {
		resource := resource

		log.Printf("Seeding %v (%v bytes) on %v", resource.path, resource.size, resource.laddr)

		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := resource.server.Serve(resource.lis); err != nil {
				if !utils.IsClosedErr(err) {
					panic(err)
				}

				return
			}
		}()
	}

	wg.Wait()
}
