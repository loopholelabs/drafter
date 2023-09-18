package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/network"
)

func main() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	firecrackerSocketPath := flag.String("firecracker-socket-path", filepath.Join(pwd, "firecracker.sock"), "Firecracker socket path (must be absolute)")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	vsockPath := flag.String("vsock-path", "vsock.sock", "VSock path")
	// agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	packagePath := flag.String("package-path", filepath.Join("out", "package"), "Path to write extracted package to")

	flag.Parse()

	// ctx, cancel := context.WithCancel(context.Background())
	// defer cancel()

	tap := network.NewTAP(
		*hostInterface,
		*hostMAC,
		*bridgeInterface,
	)

	if err := tap.Open(); err != nil {
		panic(err)
	}
	defer tap.Close()

	srv := firecracker.NewServer(
		*firecrackerBin,
		*firecrackerSocketPath,
		*packagePath,

		*verbose,
		*enableOutput,
		*enableInput,
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := srv.Wait(); err != nil {
			panic(err)
		}
	}()

	if err := srv.Open(); err != nil {
		panic(err)
	}
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *firecrackerSocketPath)
			},
		},
	}

	before := time.Now()

	if err := firecracker.ResumeSnapshot(client); err != nil {
		panic(err)
	}
	defer os.Remove(filepath.Join(*packagePath, *vsockPath))

	// handler := vsock.NewHandler(
	// 	filepath.Join(*packagePath, *vsockPath),
	// 	uint32(*agentVSockPort),

	// 	time.Second*10,
	// )

	// wg.Add(1)
	// go func() {
	// 	defer wg.Done()

	// 	if err := handler.Wait(); err != nil {
	// 		panic(err)
	// 	}
	// }()

	// peer, err := handler.Open(ctx)
	// if err != nil {
	// 	panic(err)
	// }
	// defer handler.Close()

	// if err := peer.AfterResume(ctx); err != nil {
	// 	panic(err)
	// }

	log.Println("Resume:", time.Since(before))

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	<-done

	before = time.Now()

	// if err := peer.BeforeSuspend(ctx); err != nil {
	// 	panic(err)
	// }

	if err := firecracker.FlushSnapshot(client); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))
}
