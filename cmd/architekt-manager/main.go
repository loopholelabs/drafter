package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"time"

	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

func main() {
	controlPlaneLaddr := flag.String("control-plane-laddr", ":1399", "Listen address for control plane")

	apiLaddr := flag.String("api-laddr", ":1400", "Listen address for API")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clients := 0

	svc := services.NewManager(*verbose)
	registry := rpc.NewRegistry[services.WorkerRemote, json.RawMessage](
		svc,

		time.Minute*10, // Increased timeout since this includes `CreateInstance` RPCs, which might pull for a long time
		ctx,
		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected to control plane", clients)
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				services.ManagerUnregister(svc, remoteID)

				log.Printf("%v clients connected to control plane", clients)
			},
		},
	)
	svc.ForRemotes = registry.ForRemotes

	controlPlaneLis, err := net.Listen("tcp", *controlPlaneLaddr)
	if err != nil {
		panic(err)
	}
	defer controlPlaneLis.Close()

	log.Println("Control plane listening on", controlPlaneLis.Addr())

	go func() {
		for {
			func() {
				conn, err := controlPlaneLis.Accept()
				if err != nil {
					log.Println("could not accept control plane connection, continuing:", err)

					return
				}

				go func() {
					defer func() {
						_ = conn.Close()

						if err := recover(); err != nil && !utils.IsClosedErr(err.(error)) {
							log.Printf("Control plane client disconnected with error: %v", err)
						}
					}()

					encoder := json.NewEncoder(conn)
					decoder := json.NewDecoder(conn)

					if err := registry.LinkStream(
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
					); err != nil {
						panic(err)
					}
				}()
			}()
		}
	}()

	apiLis, err := net.Listen("tcp", *apiLaddr)
	if err != nil {
		panic(err)
	}
	defer apiLis.Close()

	log.Println("API listening on", apiLis.Addr())

	panic(services.ServeManager(apiLis, svc, *verbose))
}
