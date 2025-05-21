package nat

import (
	"sync"

	"github.com/loopholelabs/drafter/pkg/network"
	loggingtypes "github.com/loopholelabs/logging/types"
)

type Namespaces struct {
	log loggingtypes.Logger

	Close func() error

	claimableNamespaces     map[string]*claimableNamespace
	claimableNamespacesLock sync.Mutex
}

type CreateNamespacesHooks struct {
	OnBeforeCreateNamespace func(id string)
	OnBeforeRemoveNamespace func(id string)
}

type claimableNamespace struct {
	namespace *network.Namespace
	claimed   bool
}

/**
 * Add a namespace to the NameSpaces
 *
 */
func (namespaces *Namespaces) addNamespace(id string, ns *network.Namespace) {
	namespaces.claimableNamespacesLock.Lock()
	defer namespaces.claimableNamespacesLock.Unlock()

	namespaces.claimableNamespaces[id] = &claimableNamespace{
		claimed:   false,
		namespace: ns,
	}
}

/**
 * Claim a namespace for exclusive use
 *
 */
func (namespaces *Namespaces) ClaimNamespace() (string, error) {
	if namespaces.log != nil {
		namespaces.log.Info().Msg("ClaimNamespace")
	}

	namespaces.claimableNamespacesLock.Lock()
	defer namespaces.claimableNamespacesLock.Unlock()

	for _, namespace := range namespaces.claimableNamespaces {
		if !namespace.claimed {
			namespace.claimed = true
			id := namespace.namespace.GetID()
			if namespaces.log != nil {
				namespaces.log.Info().Str("namespace", id).Msg("ClaimNamespace claimed")
			}
			return id, nil
		}
	}

	if namespaces.log != nil {
		namespaces.log.Error().Msg("ClaimNamespace all namespaces already claimed")
	}
	return "", ErrAllNamespacesClaimed
}

/**
 * Release a namespace for reuse
 *
 */
func (namespaces *Namespaces) ReleaseNamespace(namespace string) error {
	if namespaces.log != nil {
		namespaces.log.Info().Str("namespace", namespace).Msg("ReleaseNamespace")
	}
	namespaces.claimableNamespacesLock.Lock()
	defer namespaces.claimableNamespacesLock.Unlock()

	ns, ok := namespaces.claimableNamespaces[namespace]
	if !ok {
		if namespaces.log != nil {
			namespaces.log.Error().Str("namespace", namespace).Msg("ReleaseNamespace not found")
		}
		return nil
	}

	ns.claimed = false
	return nil
}
