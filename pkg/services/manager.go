package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

var (
	ErrNameTaken = errors.New("this name has already been taken")

	ErrCouldNotEncode         = errors.New("could not encode")
	ErrNodeNotFound           = errors.New("node not found")
	ErrCouldNotCreateInstance = errors.New("could not create instance")
	ErrCouldNotDeleteInstance = errors.New("could not delete instance")
	ErrCouldNotListInstances  = errors.New("could not list instances")
)

type Manager struct {
	ForRemotes func(
		cb func(remoteID string, remote WorkerRemote) error,
	) error

	verbose bool

	workersLock sync.Mutex
	workers     map[string]string
}

func NewManager(verbose bool) *Manager {
	return &Manager{
		verbose: verbose,

		workers: map[string]string{},
	}
}

func (g *Manager) Register(ctx context.Context, name string) error {
	if g.verbose {
		log.Printf("Register(name = %v)", name)
	}

	remoteID := rpc.GetRemoteID(ctx)

	g.workersLock.Lock()
	defer g.workersLock.Unlock()

	if _, ok := g.workers[name]; ok {
		if g.verbose {
			log.Println("Name", name, "already taken")
		}

		return ErrNameTaken
	}

	g.workers[name] = remoteID

	return nil
}

func ManagerGetWorker(g *Manager, name string) *WorkerRemote {
	g.workersLock.Lock()

	remoteID, ok := g.workers[name]
	if !ok {
		g.workersLock.Unlock()

		return nil
	}

	g.workersLock.Unlock()

	// We can safely ignore the errors here, since errors are bubbled up from `cb`,
	// which can never return an error here
	var remote WorkerRemote
	_ = g.ForRemotes(func(candidateID string, candidate WorkerRemote) error {
		if candidateID == remoteID {
			remote = candidate
		}

		return nil
	})

	return &remote
}

func ManagerUnregister(g *Manager, remoteID string) {
	g.workersLock.Lock()
	defer g.workersLock.Unlock()

	for name, candidateID := range g.workers {
		if candidateID == remoteID {
			delete(g.workers, name)

			break
		}
	}
}

func ManagerGetWorkerNames(g *Manager) []string {
	g.workersLock.Lock()
	defer g.workersLock.Unlock()

	names := []string{}
	for name := range g.workers {
		names = append(names, name)
	}

	return names
}

func ServeManager(lis net.Listener, svc *Manager, verbose bool) error {
	r := mux.NewRouter()

	r.HandleFunc(
		"/nodes",
		func(w http.ResponseWriter, r *http.Request) {
			if verbose {
				log.Println("Listing nodes")
			}

			nodes := ManagerGetWorkerNames(svc)

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(nodes); err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}
		},
	).Methods("GET")

	r.HandleFunc(
		"/nodes/{nodeName}/instances",
		func(w http.ResponseWriter, r *http.Request) {
			nodeName := mux.Vars(r)["nodeName"]
			if nodeName == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if verbose {
				log.Println("Listing instances on node", nodeName)
			}

			remote := ManagerGetWorker(svc, nodeName)
			if remote == nil {
				log.Println(ErrNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}

			packageRaddrs, err := remote.ListInstances(r.Context())
			if err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotListInstances, err))

				w.WriteHeader(http.StatusInternalServerError)
			}

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(packageRaddrs); err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}
		},
	).Methods("GET")

	r.HandleFunc(
		"/nodes/{nodeName}/instances/{packageRaddr}",
		func(w http.ResponseWriter, r *http.Request) {
			nodeName, packageRaddr := mux.Vars(r)["nodeName"], mux.Vars(r)["packageRaddr"]
			if nodeName == "" || packageRaddr == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if verbose {
				log.Println("Creating instance on node", nodeName, "from package raddr", packageRaddr)
			}

			remote := ManagerGetWorker(svc, nodeName)
			if remote == nil {
				log.Println(ErrNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}

			outputPackageRaddr, err := remote.CreateInstance(r.Context(), packageRaddr)
			if err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotCreateInstance, err))

				w.WriteHeader(http.StatusInternalServerError)
			}

			w.Header().Set("Content-Type", "application/json")

			if err := json.NewEncoder(w).Encode(outputPackageRaddr); err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotEncode, err))

				w.WriteHeader(http.StatusInternalServerError)

				return
			}
		},
	).Methods("POST")

	r.HandleFunc(
		"/nodes/{nodeName}/instances/{packageRaddr}",
		func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			nodeName, packageRaddr := vars["nodeName"], vars["packageRaddr"]
			if nodeName == "" || packageRaddr == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)

				return
			}

			if verbose {
				log.Println("Deleting instance", packageRaddr, "from node", nodeName)
			}

			remote := ManagerGetWorker(svc, nodeName)
			if remote == nil {
				log.Println(ErrNodeNotFound)

				w.WriteHeader(http.StatusNotFound)

				return
			}

			if err := remote.DeleteInstance(r.Context(), packageRaddr); err != nil {
				log.Println(fmt.Errorf("%w: %w", ErrCouldNotDeleteInstance, err))

				w.WriteHeader(http.StatusInternalServerError)
			}
		},
	).Methods("DELETE")

	return http.Serve(lis, r)
}
