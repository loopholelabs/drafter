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
	"github.com/loopholelabs/architekt/pkg/config"
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

	packageFile, err := os.OpenFile(*packagePath, os.O_RDONLY, os.ModePerm)
	if err != nil {
		panic(err)
	}
	defer packageFile.Close()

	packageArchive := tar.NewReader(packageFile)

	packageConfig, _, err := utils.ReadPackageConfigFromTar(packageArchive, config.PackageConfigName)
	if err != nil {
		panic(err)
	}

	if err := packageFile.Close(); err != nil {
		panic(err)
	}

	resources := []*resources{
		{
			laddr: *initramfsLaddr,
			path:  config.InitramfsName,
		},
		{
			laddr: *kernelLaddr,
			path:  config.KernelName,
		},
		{
			laddr: *diskLaddr,
			path:  config.DiskName,
		},

		{
			laddr: *stateLaddr,
			path:  config.StateName,
		},
		{
			laddr: *memoryLaddr,
			path:  config.MemoryName,
		},
	}
	for _, rsc := range resources {
		r := rsc

		f, err := os.OpenFile(*packagePath, os.O_RDONLY, os.ModePerm)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		off, size, err := utils.FindSectionForPathInArchive(f, r.path)
		if err != nil {
			panic(err)
		}
		r.size = size

		b := backend.NewReadOnlyBackend(utils.NewSectionReaderAt(f, off, size), r.size)

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
