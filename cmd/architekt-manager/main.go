package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

var (
	errCouldNotEncode         = errors.New("could not encode")
	errCouldNotFetchNodes     = errors.New("could not fetch nodes")
	errCouldNotFetchInstances = errors.New("could not fetch instances")
	errNodeNotFound           = errors.New("node not found")
	errCouldNotCreateInstance = errors.New("could not create instance")
	errCouldNotDeleteInstance = errors.New("could not delete instance")
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

		time.Minute*10, // Increased timeout since this includes `CreateInstance` RPCs, which might pull for a long time
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

			nodes := []string{}
			if err := registry.ForRemotes(func(remoteID string, remote services.WorkerRemote) error {
				nodes = append(nodes, remoteID)

				return nil
			}); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotFetchNodes, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(nodes); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
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

			var packageRaddrs []string
			if err := registry.ForRemotes(func(remoteID string, remote services.WorkerRemote) error {
				if remoteID == nodeID {
					r, err := remote.ListInstances(r.Context())
					if err != nil {
						return err
					}

					packageRaddrs = r
				}

				return nil
			}); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotFetchInstances, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			if packageRaddrs == nil {
				log.Println(errNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(packageRaddrs); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}
		},
	).Methods("GET")

	r.HandleFunc(
		"/nodes/{nodeID}/instances/{packageRaddr}",
		func(w http.ResponseWriter, r *http.Request) {
			nodeID, packageRaddr := mux.Vars(r)["nodeID"], mux.Vars(r)["packageRaddr"]
			if nodeID == "" || packageRaddr == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Creating instance on node", nodeID, "from package raddr", packageRaddr)
			}

			outputPackageRaddr := ""
			if err := registry.ForRemotes(func(remoteID string, remote services.WorkerRemote) error {
				if remoteID == nodeID {
					i, err := remote.CreateInstance(r.Context(), packageRaddr)
					if err != nil {
						return err
					}

					outputPackageRaddr = i
				}

				return nil
			}); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotCreateInstance, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			if outputPackageRaddr == "" {
				log.Println(errNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(outputPackageRaddr); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}
		},
	).Methods("POST")

	r.HandleFunc(
		"/nodes/{nodeID}/instances/{packageRaddr}",
		func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			nodeID, packageRaddr := vars["nodeID"], vars["packageRaddr"]
			if nodeID == "" || packageRaddr == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if *verbose {
				log.Println("Deleting instance", packageRaddr, "from node", nodeID)
			}

			found := false
			if err := registry.ForRemotes(func(remoteID string, remote services.WorkerRemote) error {
				if remoteID == nodeID {
					if err := remote.DeleteInstance(r.Context(), packageRaddr); err != nil {
						return err
					}

					found = true
				}

				return nil
			}); err != nil {
				log.Println(fmt.Errorf("%w: %w", errCouldNotDeleteInstance, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			if !found {
				log.Println(errNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}
		},
	).Methods("DELETE")

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
