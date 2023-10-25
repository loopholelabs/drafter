package services

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/pojntfx/dudirekta/pkg/rpc"
)

var (
	ErrNameTaken = errors.New("this name has already been taken")
)

type ManagerRemote struct {
	Register func(ctx context.Context, name string) error
}

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
