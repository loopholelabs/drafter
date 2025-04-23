package nat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/pkg/network"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

type TranslationConfiguration struct {
	HostInterface             string `json:"hostInterface"`
	HostVethCIDR              string `json:"hostVethCIDR"`
	NamespaceVethCIDR         string `json:"namespaceVethCIDR"`
	BlockedSubnetCIDR         string `json:"blockedSubnetCIDR"`
	NamespaceInterface        string `json:"namespaceInterface"`
	NamespaceInterfaceGateway string `json:"namespaceInterfaceGateway"`
	NamespaceInterfaceNetmask uint32 `json:"namespaceInterfaceNetmask"`
	NamespaceInterfaceIP      string `json:"namespaceInterfaceIP"`
	NamespaceInterfaceMAC     string `json:"namespaceInterfaceMAC"`
	NamespacePrefix           string `json:"namespacePrefix"`
	AllowIncomingTraffic      bool   `json:"allowIncomingTraffic"`
}

/**
 * Create a nat
 *
 */
func CreateNAT(ctx context.Context, rescueCtx context.Context, translationConfiguration TranslationConfiguration,
	hooks CreateNamespacesHooks, maxCreated int) (namespaces *Namespaces, errs error) {

	// Check if the host interface exists
	_, err := net.InterfaceByName(translationConfiguration.HostInterface)
	if err != nil {
		return nil, errors.Join(ErrCouldNotFindHostInterface, err)
	}

	err = network.CreateNAT(translationConfiguration.HostInterface)
	if err != nil {
		return nil, errors.Join(ErrCouldNotCreateNAT, err)
	}

	natCtx, natCancel := context.WithCancel(ctx)
	defer natCancel()

	hostVethIPs := network.NewIPTable(translationConfiguration.HostVethCIDR, natCtx)
	err = hostVethIPs.Open(natCtx)
	if err != nil {
		return nil, errors.Join(ErrCouldNotOpenHostVethIPs, err)
	}

	namespaceVethIPs := network.NewIPTable(translationConfiguration.NamespaceVethCIDR, natCtx)
	err = namespaceVethIPs.Open(natCtx)
	if err != nil {
		return nil, errors.Join(ErrCouldNotOpenNamespaceVethIPs, err)
	}

	availableIPs := namespaceVethIPs.AvailableIPs()

	if availableIPs > hostVethIPs.AvailablePairs() {
		return nil, ErrNotEnoughAvailableIPsInHostCIDR
	}

	if availableIPs < 1 {
		return nil, ErrNotEnoughAvailableIPsInNamespaceCIDR
	}

	// Reduce the number of namespaces created...
	if uint64(maxCreated) < availableIPs {
		availableIPs = uint64(maxCreated)
	}

	// FIXME: Tidy up under here

	namespaces = &Namespaces{
		Close: func() error {
			return nil
		},
		claimableNamespaces: map[string]*claimableNamespace{},
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	var (
		hostVeths          []*network.IPPair
		hostVethsLock      sync.Mutex
		namespaceVeths     []*network.IP
		namespaceVethsLock sync.Mutex
	)

	var closeLock sync.Mutex
	closed := false

	namespaces.Close = func() (errs error) {

		hostVethsLock.Lock()
		defer hostVethsLock.Unlock()

		for _, hostVeth := range hostVeths {
			err := namespaceVethIPs.ReleasePair(rescueCtx, hostVeth)
			if err != nil {
				errs = errors.Join(errs, ErrCouldNotReleaseHostVethIP, err)
			}
		}

		hostVeths = []*network.IPPair{}

		namespaceVethsLock.Lock()
		defer namespaceVethsLock.Unlock()

		for _, namespaceVeth := range namespaceVeths {
			if err := namespaceVethIPs.ReleaseIP(rescueCtx, namespaceVeth); err != nil {
				errs = errors.Join(errs, ErrCouldNotReleaseNamespaceVethIP, err)
			}
		}

		namespaceVeths = []*network.IP{}

		namespaces.claimableNamespacesLock.Lock()
		defer namespaces.claimableNamespacesLock.Unlock()

		for _, claimableNamespace := range namespaces.claimableNamespaces {
			if hook := hooks.OnBeforeRemoveNamespace; hook != nil {
				hook(claimableNamespace.namespace.GetID())
			}

			if err := claimableNamespace.namespace.Close(); err != nil {
				errs = errors.Join(errs, ErrCouldNotCloseNamespace, err)
			}
		}

		namespaces.claimableNamespaces = map[string]*claimableNamespace{}

		closeLock.Lock()
		defer closeLock.Unlock()

		if !closed {
			closed = true

			if err := network.RemoveNAT(translationConfiguration.HostInterface); err != nil {
				errs = errors.Join(errs, ErrCouldNotRemoveNAT, err)
			}
		}

		// No need to call `.Wait()` here since `.Wait()` is just waiting for us to cancel the in-progress context

		return
	}

	ready := make(chan struct{})
	// This goroutine will not leak on function return because it selects on `goroutineManager.Context().Done()` internally
	goroutineManager.StartBackgroundGoroutine(func(internalCtx context.Context) {
		select {
		// Failure case; something failed and the goroutineManager.Context() was cancelled before were ready
		case <-internalCtx.Done():
			if err := namespaces.Close(); err != nil {
				panic(errors.Join(ErrNATContextCancelled, err))
			}

		// Happy case; we've set up all of the namespaces and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-ready:
			<-ctx.Done()

			if err := namespaces.Close(); err != nil {
				panic(errors.Join(ErrNATContextCancelled, err))
			}

			break
		}
	})

	for i := uint64(0); i < availableIPs; i++ {
		id := fmt.Sprintf("%v%v", translationConfiguration.NamespacePrefix, i)

		var hostVeth *network.IPPair
		if err := func() error {
			hostVethsLock.Lock()
			defer hostVethsLock.Unlock()

			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				break
			}

			var err error
			hostVeth, err = hostVethIPs.GetPair(goroutineManager.Context())
			if err != nil {
				if e := namespaceVethIPs.ReleasePair(rescueCtx, hostVeth); e != nil {
					return errors.Join(ErrCouldNotReleaseHostVethIP, err, e)
				}

				return err
			}

			hostVeths = append(hostVeths, hostVeth)

			return nil
		}(); err != nil {
			panic(errors.Join(ErrCouldNotOpenHostVethIPs, err))
		}

		var namespaceVeth *network.IP
		if err := func() error {
			namespaceVethsLock.Lock()
			defer namespaceVethsLock.Unlock()

			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				break
			}

			var err error
			namespaceVeth, err = namespaceVethIPs.GetIP(goroutineManager.Context())
			if err != nil {
				if e := namespaceVethIPs.ReleaseIP(rescueCtx, namespaceVeth); e != nil {
					return errors.Join(ErrCouldNotReleaseNamespaceVethIP, err, e)
				}

				return err
			}

			namespaceVeths = append(namespaceVeths, namespaceVeth)

			return nil
		}(); err != nil {
			panic(errors.Join(ErrCouldNotOpenNamespaceVethIPs, err))
		}

		if err := func() error {

			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				break
			}

			if hook := hooks.OnBeforeCreateNamespace; hook != nil {
				hook(id)
			}

			nsConf := &network.NamespaceConfig{
				HostInterface:             translationConfiguration.HostInterface,
				NamespaceInterface:        translationConfiguration.NamespaceInterface,
				NamespaceInterfaceGateway: translationConfiguration.NamespaceInterfaceGateway,
				NamespaceInterfaceNetmask: translationConfiguration.NamespaceInterfaceNetmask,
				HostVethInternalIP:        hostVeth.GetFirstIP().String(),
				HostVethExternalIP:        hostVeth.GetSecondIP().String(),
				NamespaceInterfaceIP:      translationConfiguration.NamespaceInterfaceIP,
				NamespaceVethIP:           namespaceVeth.String(),
				BlockedSubnet:             translationConfiguration.BlockedSubnetCIDR,
				NamespaceInterfaceMAC:     translationConfiguration.NamespaceInterfaceMAC,
				AllowIncomingTraffic:      translationConfiguration.AllowIncomingTraffic,
			}
			namespace := network.NewNamespace(id, nsConf)
			if err := namespace.Open(); err != nil {
				if e := namespace.Close(); e != nil {
					return errors.Join(ErrCouldNotOpenNamespace, err, e)
				}

				return err
			}

			namespaces.addNamespace(id, namespace)

			return nil
		}(); err != nil {
			panic(errors.Join(ErrCouldNotOpenNamespace, err))
		}
	}

	close(ready) // We can safely close() this channel since this code path will only run once

	return
}
