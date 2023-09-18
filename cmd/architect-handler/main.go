package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/vsock"
)

func main() {
	vsockPath := flag.String("vsock-path", filepath.Join("out", "package", "vsock.sock"), "VSock path")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	beforeSuspend := flag.Bool("before-suspend", false, "Whether to run the BeforeSuspend handler")
	afterResume := flag.Bool("after-resume", false, "Whether to run the AfterResume handler")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := vsock.NewHandler(
		*vsockPath,
		uint32(*agentVSockPort),

		time.Second*10,
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := handler.Wait(); err != nil {
			panic(err)
		}
	}()

	peer, err := handler.Open(ctx)
	if err != nil {
		panic(err)
	}
	defer handler.Close()

	log.Println("Connected to", *vsockPath, *agentVSockPort)

	if *beforeSuspend {
		if err := peer.BeforeSuspend(ctx); err != nil {
			panic(err)
		}
	}

	if *afterResume {
		if err := peer.AfterResume(ctx); err != nil {
			panic(err)
		}
	}
}
