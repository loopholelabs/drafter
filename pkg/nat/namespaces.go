package nat

import (
	"sync"

	"github.com/loopholelabs/drafter/internal/network"
)

type claimableNamespace struct {
	namespace *network.Namespace
	claimed   bool
}

type Namespaces struct {
	Wait  func() error
	Close func() error

	claimableNamespaces     map[string]*claimableNamespace
	claimableNamespacesLock sync.Mutex
}

type CreateNamespacesHooks struct {
	OnBeforeCreateNamespace func(id string)
	OnBeforeRemoveNamespace func(id string)
}

func (namespaces *Namespaces) ReleaseNamespace(namespace string) error {
	namespaces.claimableNamespacesLock.Lock()
	defer namespaces.claimableNamespacesLock.Unlock()

	ns, ok := namespaces.claimableNamespaces[namespace]
	if !ok {
		// Releasing non-claimed namespaces is a no-op
		return nil
	}

	ns.claimed = false

	return nil
}

func (namespaces *Namespaces) ClaimNamespace() (string, error) {
	namespaces.claimableNamespacesLock.Lock()
	defer namespaces.claimableNamespacesLock.Unlock()

	for _, namespace := range namespaces.claimableNamespaces {
		if !namespace.claimed {
			namespace.claimed = true

			return namespace.namespace.GetID(), nil
		}
	}

	return "", ErrAllNamespacesClaimed
}
