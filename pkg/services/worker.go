package services

import (
	"context"
	"log"
)

type HypervisorConfiguration struct {
	FirecrackerBin string
	JailerBin      string

	ChrootBaseDir string

	UID int
	GID int

	NumaNode      int
	CgroupVersion int

	EnableOutput bool
	EnableInput  bool
}

type WorkerRemote struct {
	ListInstances  func(ctx context.Context) ([]string, error)
	CreateInstance func(ctx context.Context, packageRaddr string) (string, error)
	DeleteInstance func(ctx context.Context, packageRaddr string) error
}

type Worker struct {
	verbose bool

	claimNetworkNamespace   func() (string, error)
	releaseNetworkNamespace func(namespace string) error

	hypervisorConfiguration HypervisorConfiguration
}

func NewWorker(
	verbose bool,

	claimNetworkNamespace func() (string, error),
	releaseNetworkNamespace func(namespace string) error,

	hypervisorConfiguration HypervisorConfiguration,
) *Worker {
	return &Worker{
		verbose: verbose,

		claimNetworkNamespace:   claimNetworkNamespace,
		releaseNetworkNamespace: releaseNetworkNamespace,

		hypervisorConfiguration: hypervisorConfiguration,
	}
}

func (w *Worker) ListInstances(ctx context.Context) ([]string, error) {
	if w.verbose {
		log.Println("ListInstances()")
	}

	return []string{}, nil
}

func (w *Worker) CreateInstance(ctx context.Context, packageRaddr string) (string, error) {
	if w.verbose {
		log.Printf("CreateInstance(packageRaddr = %v)", packageRaddr)
	}

	return "", nil
}

func (w *Worker) DeleteInstance(ctx context.Context, packageRaddr string) error {
	if w.verbose {
		log.Printf("DeleteInstance(packageRaddr = %v)", packageRaddr)
	}

	return nil
}
