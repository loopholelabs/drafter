package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	v1 "github.com/pojntfx/r3map/pkg/api/proto/migration/v1"
	"github.com/pojntfx/r3map/pkg/migration"
	"github.com/pojntfx/r3map/pkg/services"
	"github.com/pojntfx/r3map/pkg/utils"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	agentVSockPort := flag.Uint("agent-vsock-port", 26, "Agent VSock port")

	size := flag.Int64("size", 10737418240, "Size of the resource")

	raddr := flag.String("raddr", "localhost:1338", "Remote address")
	laddr := flag.String("laddr", "localhost:1338", "Listen address")

	singleHost := flag.Bool("single-host", false, "Whether to enable single-host mode which only starts resources ad-hoc")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := roles.NewRunner(
		roles.HypervisorConfiguration{
			FirecrackerBin: *firecrackerBin,

			Verbose:      *verbose,
			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},
		roles.NetworkConfiguration{
			HostInterface:   *hostInterface,
			HostMAC:         *hostMAC,
			BridgeInterface: *bridgeInterface,
		},
		roles.AgentConfiguration{
			AgentVSockPort: uint32(*agentVSockPort),
		},
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := runner.Wait(); err != nil {
			panic(err)
		}
	}()

	if !*singleHost {
		defer runner.Close()
		if err := runner.Open(); err != nil {
			panic(err)
		}
	}

	f, err := os.CreateTemp("", "")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(f.Name())

	if err := f.Truncate(*size); err != nil {
		panic(err)
	}

	bar := progressbar.NewOptions(
		int(*size),
		progressbar.OptionSetDescription("Pulling"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionFullWidth(),
		// VT-100 compatibility
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	bar.Add(client.MaximumBlockSize)

	mgr := migration.NewPathMigrator(
		ctx,

		backend.NewFileBackend(f),

		&migration.MigratorOptions{
			Verbose: *verbose,
		},
		&migration.MigratorHooks{
			OnBeforeSync: func() error {
				before := time.Now()
				defer func() {
					log.Println("Suspend:", time.Since(before))
				}()

				log.Println("Suspending VM")

				if *singleHost {
					if err := runner.Suspend(ctx); err != nil {
						return err
					}

					return runner.Close()
				}

				return runner.Suspend(ctx)
			},
			OnAfterSync: func(dirtyOffsets []int64) error {
				bar.Clear()

				delta := (len(dirtyOffsets) * client.MaximumBlockSize)

				log.Printf("Invalidated: %.2f MB (%.2f Mb)", float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

				bar.ChangeMax(int(*size) + delta)

				bar.Describe("Finalizing")

				return nil
			},

			OnBeforeClose: func() error {
				log.Println("Stopping VM")

				return runner.Close()
			},

			OnChunkIsLocal: func(off int64) error {
				bar.Add(client.MaximumBlockSize)

				return nil
			},
		},

		nil,
		nil,
	)

	finished := make(chan struct{})
	go func() {
		defer close(finished)

		if err := mgr.Wait(); err != nil {
			panic(err)
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done

		log.Println("Exiting gracefully")

		_ = mgr.Close()
	}()

	var (
		file string
		svc  *services.SeederService
	)

	conn, err := grpc.Dial(*raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Leeching from", *raddr)

	defer mgr.Close()
	finalize, _, err := mgr.Leech(services.NewSeederRemoteGrpc(v1.NewSeederClient(conn)))
	if err != nil {
		panic(err)
	}

	log.Println("Press <ENTER> to finalize migration")

	continueCh := make(chan struct{})
	go func() {
		bufio.NewScanner(os.Stdin).Scan()

		continueCh <- struct{}{}
	}()

	select {
	case <-continueCh:
	case <-finished:
		return
	}

	before := time.Now()

	seed, packagePath, err := finalize()
	if err != nil {
		panic(err)
	}
	file = packagePath

	bar.Clear()

	log.Println("Resuming VM on", file)

	if *singleHost {
		defer runner.Close()
		if err := runner.Open(); err != nil {
			panic(err)
		}
	}

	if err := runner.Resume(ctx, file); err != nil {
		panic(err)
	}

	log.Println("Resume:", time.Since(before))

	svc, err = seed()
	if err != nil {
		panic(err)
	}

	server := grpc.NewServer()

	v1.RegisterSeederServer(server, services.NewSeederServiceGrpc(svc))

	lis, err := net.Listen("tcp", *laddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Seeding on", *laddr)

	go func() {
		if err := server.Serve(lis); err != nil {
			if !utils.IsClosedErr(err) {
				panic(err)
			}

			return
		}
	}()

	<-finished
}