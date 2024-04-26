package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"strings"

	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/drafter/pkg/vsock"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

type local struct{}

func (s *local) BeforeSuspend(ctx context.Context) error {
	log.Println("BeforeSuspend()")

	return nil
}

func (s *local) AfterResume(ctx context.Context) error {
	log.Println("AfterResume()")

	return nil
}

func main() {
	vsockPort := flag.Uint("vsock-port", 26, "VSock port")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clients := 0

	registry := rpc.NewRegistry[struct{}, json.RawMessage](
		&local{},

		ctx,

		&rpc.Options{
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

	for {
		if err := func() error {
			conn, err := vsock.Dial(vsock.CIDHost, uint32(*vsockPort))
			if err != nil {
				return err
			}
			defer conn.Close()

			log.Println("Connected to VSock CID", vsock.CIDHost, "and port", *vsockPort)

			encoder := json.NewEncoder(conn)
			decoder := json.NewDecoder(conn)

			return registry.LinkStream(
				func(v rpc.Message[json.RawMessage]) error {
					return encoder.Encode(v)
				},
				func(v *rpc.Message[json.RawMessage]) error {
					return decoder.Decode(v)
				},

				func(v any) (json.RawMessage, error) {
					b, err := json.Marshal(v)
					if err != nil {
						return nil, err
					}

					return json.RawMessage(b), nil
				},
				func(data json.RawMessage, v any) error {
					return json.Unmarshal([]byte(data), v)
				},
			)
		}(); err != nil && !utils.IsClosedErr(err) && !strings.HasSuffix(err.Error(), "connection timed out") {
			panic(err)
		}

		log.Println("Disconnected from host, reconnecting")
	}
}
