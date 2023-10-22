package main

import (
	"flag"
	"log"
	"net"
	"os"
	"path/filepath"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	iservices "github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	iutils "github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/r3map/pkg/services"
	"google.golang.org/grpc"
)

func main() {
	packagePath := flag.String("package-path", filepath.Join("out", "redis.ark"), "Path to package to serve")

	laddr := flag.String("laddr", "localhost:1337", "Listen address")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	size, err := iutils.GetFileSize(*packagePath)
	if err != nil {
		panic(err)
	}

	f, err := os.Open(*packagePath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	packageConfig, err := utils.ReadPackageConfigFromEXT4Filesystem(f)
	if err != nil {
		panic(err)
	}

	b := backend.NewFileBackend(f)

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

	v1.RegisterSeederWithMetaServer(server, iservices.NewSeederWithMetaServiceGrpc(svc))

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Seeding", size, "bytes on", *laddr)

	if err := server.Serve(lis); err != nil {
		if !utils.IsClosedErr(err) {
			panic(err)
		}

		return
	}
}
