package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/mdlayher/vsock"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

func main() {
	vsockPort := flag.Int("vsock-port", 26, "VSock port")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := services.NewAgent(
		func(ctx context.Context) error {
			log.Println("Suspending app")

			return nil
		},
		func(ctx context.Context) error {
			log.Println("Resumed app")

			return nil
		},
	)

	clients := 0
	registry := rpc.NewRegistry(
		svc,
		struct{}{},

		time.Second*10,
		ctx,
		&rpc.Options{
			ResponseBufferLen: rpc.DefaultResponseBufferLen,
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected", clients)
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				log.Printf("%v clients connected", clients)
			},
		},
	)

	lis, err := vsock.ListenContextID(firecracker.CIDGuest, uint32(*vsockPort), nil)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Listening on", lis.Addr())

	for {
		func() {
			conn, err := lis.Accept()
			if err != nil {
				log.Println("could not accept connection, continuing:", err)

				return
			}

			go func() {

				defer func() {
					_ = conn.Close()

					if err := recover(); err != nil {
						log.Printf("Client disconnected with error: %v", err)
					}
				}()

				if err := registry.Link(conn); err != nil {
					panic(err)
				}
			}()
		}()
	}
}
