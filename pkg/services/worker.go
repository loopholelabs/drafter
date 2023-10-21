package services

import (
	"context"
	"log"

	"github.com/loopholelabs/architekt/pkg/utils"
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
	ListInstances   func(ctx context.Context) ([]string, error)
	CreateInstance  func(ctx context.Context, packageRaddr string) (string, error)
	DeleteInstance  func(ctx context.Context, instanceID string) error
	MigrateInstance func(ctx context.Context, instanceID string, sourceNodeID string) error
}

type Worker struct {
	verbose bool

	claimNetworkNamespace   func() (string, error)
	releaseNetworkNamespace func(namespace string) error

	hypervisorConfiguration HypervisorConfiguration
	agentConfiguration      utils.AgentConfiguration
}

func NewWorker(
	verbose bool,

	claimNetworkNamespace func() (string, error),
	releaseNetworkNamespace func(namespace string) error,

	hypervisorConfiguration HypervisorConfiguration,
	agentConfiguration utils.AgentConfiguration,
) *Worker {
	return &Worker{
		verbose: verbose,

		claimNetworkNamespace:   claimNetworkNamespace,
		releaseNetworkNamespace: releaseNetworkNamespace,

		hypervisorConfiguration: hypervisorConfiguration,
		agentConfiguration:      agentConfiguration,
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

func (w *Worker) DeleteInstance(ctx context.Context, instanceID string) error {
	if w.verbose {
		log.Printf("CreateInstance(instanceID = %v)", instanceID)
	}

	return nil
}

func (w *Worker) MigrateInstance(ctx context.Context, instanceID string, sourceNodeID string) error {
	if w.verbose {
		log.Printf("MigrateInstance(instanceID = %v, sourceNodeID = %v)", instanceID, sourceNodeID)
	}

	return nil
}
