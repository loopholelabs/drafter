package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/gorilla/mux"
)

func main() {
	laddr := flag.String("laddr", ":1339", "Listen address")
	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

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

	log.Println("Listening on", *laddr)

	panic(http.ListenAndServe(*laddr, r))
}
