package main

import (
	"context"
	"flag"
	"log"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/vsock"
)

func main() {
	vsockPort := flag.Int("vsock-port", 26, "VSock port")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := vsock.NewAgent(
		vsock.CIDGuest,
		uint32(*vsockPort),

		func(ctx context.Context) error {
			log.Println("Suspending app")

			return nil
		},
		func(ctx context.Context) error {
			log.Println("Resumed app")

			return nil
		},

		time.Second*10,

		*verbose,
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := agent.Wait(); err != nil {
			panic(err)
		}
	}()

	if err := agent.Open(ctx); err != nil {
		panic(err)
	}
	defer agent.Close()

	log.Println("Listening on", *vsockPort)

	wg.Wait()
}
