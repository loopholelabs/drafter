package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
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

	registry := rpc.NewRegistry(
		struct{}{},
		services.WorkerRemote{},

		time.Second*10,
		ctx,
		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected to control plane", clients)
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				log.Printf("%v clients connected to control plane", clients)
			},
		},
	)

	r := mux.NewRouter()

	r.HandleFunc(
		"/nodes",
		func(w http.ResponseWriter, r *http.Request) {
			if *verbose {
				log.Println("Listing nodes")
			}
		},
	).Methods("GET")

	r.HandleFunc(
		"/nodes/{nodeID}/instances",
		func(w http.ResponseWriter, r *http.Request) {
			nodeID := mux.Vars(r)["nodeID"]
			if nodeID == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Listing instances on node", nodeID)
			}
		},
	).Methods("GET")

	r.HandleFunc(
		"/nodes/{nodeID}/instances",
		func(w http.ResponseWriter, r *http.Request) {
			nodeID, packageRaddr := mux.Vars(r)["nodeID"], r.URL.Query().Get("packageRaddr")
			if nodeID == "" || packageRaddr == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Creating instance on node", nodeID, "from package raddr", packageRaddr)
			}
		},
	).Methods("POST")

	r.HandleFunc(
		"/nodes/{nodeID}/instances/{instanceID}",
		func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			nodeID, instanceID := vars["nodeID"], vars["instanceID"]
			if nodeID == "" || instanceID == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Deleting instance", instanceID, "from node", nodeID)
			}
		},
	).Methods("DELETE")

	r.HandleFunc(
		"/nodes/{destinationNodeID}/instances/{instanceID}",
		func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			instanceID, sourceNodeID, destinationNodeID := vars["instanceID"], r.URL.Query().Get("sourceNodeID"), vars["destinationNodeID"]
			if destinationNodeID == "" || instanceID == "" || sourceNodeID == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Migrating instance", instanceID, "from source node", sourceNodeID, "to destination node", destinationNodeID)
			}
		},
	).Methods("POST")

	lis, err := net.Listen("tcp", *controlPlaneLaddr)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Control plane listening on", lis.Addr())

	go func() {
		for {
			func() {
				conn, err := lis.Accept()
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

					if err := registry.LinkStream(
						json.NewEncoder(conn).Encode,
						json.NewDecoder(conn).Decode,

						json.Marshal,
						json.Unmarshal,
					); err != nil {
						panic(err)
					}
				}()
			}()
		}
	}()

	log.Println("API listening on", *apiLaddr)

	panic(http.ListenAndServe(*apiLaddr, r))
}
