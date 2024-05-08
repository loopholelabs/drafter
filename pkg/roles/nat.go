package roles

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/pkg/network"
	"github.com/loopholelabs/drafter/pkg/utils"
)

var (
	ErrNotEnoughAvailableIPsInHostCIDR      = errors.New("not enough available IPs in host CIDR")
	ErrNotEnoughAvailableIPsInNamespaceCIDR = errors.New("not enough available IPs in namespace CIDR")
	ErrAllNamespacesClaimed                 = errors.New("all namespaces claimed")
)

type claimableNamespace struct {
	namespace *network.Namespace
	claimed   bool
}

type Namespaces struct {
	ReleaseNamespace func(namespace string) error
	ClaimNamespace   func() (string, error)

	Wait  func() error
	Close func() error
}

type CreateNamespacesHooks struct {
	OnBeforeCreateNamespace func(id string)
	OnBeforeRemoveNamespace func(id string)
}

func CreateNAT(
	ctx context.Context,
	closeCtx context.Context,
	hostInterface string,

	hostVethCIDR string,
	namespaceVethCIDR string,

	namespaceInterface string,
	namespaceInterfaceGateway string,
	namespaceInterfaceNetmask uint32,
	namespaceInterfaceIP string,
	namespaceInterfaceMAC string,

	namespacePrefix string,

	hooks CreateNamespacesHooks,
) (namespaces *Namespaces, errs error) {
	namespaces = &Namespaces{}

	// We use the background context here instead of the internal context because we want to distinguish
	// between a context cancellation from the outside and getting a response
	readyCtx, cancelReadyCtx := context.WithCancel(ctx)
	defer cancelReadyCtx()

	internalCtx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	// Check if the host interface exists
	if _, err := net.InterfaceByName(hostInterface); err != nil {
		panic(err)
	}

	if err := network.CreateNAT(hostInterface); err != nil {
		panic(err)
	}

	hostVethIPs := network.NewIPTable(hostVethCIDR, internalCtx)
	if err := hostVethIPs.Open(internalCtx); err != nil {
		panic(err)
	}

	namespaceVethIPs := network.NewIPTable(namespaceVethCIDR, internalCtx)

	if err := namespaceVethIPs.Open(internalCtx); err != nil {
		panic(err)
	}

	if namespaceVethIPs.AvailableIPs() > hostVethIPs.AvailablePairs() {
		panic(ErrNotEnoughAvailableIPsInHostCIDR)
	}

	availableIPs := namespaceVethIPs.AvailableIPs()
	if availableIPs < 1 {
		panic(ErrNotEnoughAvailableIPsInNamespaceCIDR)
	}

	var (
		hostVeths     []*network.IPPair
		hostVethsLock sync.Mutex

		namespaceVeths     []*network.IP
		namespaceVethsLock sync.Mutex

		claimableNamespaces     = map[string]claimableNamespace{}
		claimableNamespacesLock sync.Mutex
	)

	var closeLock sync.Mutex
	closed := false

	closeInProgressContext, cancelCloseInProgressContext := context.WithCancel(closeCtx) // We use `closeContext` here since this simply intercepts `ctx`
	namespaces.Close = func() (errs error) {
		defer cancelCloseInProgressContext()

		hostVethsLock.Lock()
		defer hostVethsLock.Unlock()

		for _, hostVeth := range hostVeths {
			if err := namespaceVethIPs.ReleasePair(closeCtx, hostVeth); err != nil {
				errs = errors.Join(errs, err)
			}
		}

		hostVeths = []*network.IPPair{}

		namespaceVethsLock.Lock()
		defer namespaceVethsLock.Unlock()

		for _, namespaceVeth := range namespaceVeths {
			if err := namespaceVethIPs.ReleaseIP(closeCtx, namespaceVeth); err != nil {
				errs = errors.Join(errs, err)
			}
		}

		namespaceVeths = []*network.IP{}

		claimableNamespacesLock.Lock()
		defer claimableNamespacesLock.Unlock()

		for _, claimableNamespace := range claimableNamespaces {
			if hook := hooks.OnBeforeRemoveNamespace; hook != nil {
				hook(claimableNamespace.namespace.GetID())
			}

			if err := claimableNamespace.namespace.Close(); err != nil {
				errs = errors.Join(errs, err)
			}
		}

		claimableNamespaces = map[string]claimableNamespace{}

		closeLock.Lock()
		defer closeLock.Unlock()

		if !closed {
			if err := network.RemoveNAT(hostInterface); err != nil {
				errs = errors.Join(errs, err)
			}

			closed = true
		}

		// No need to call `.Wait()` here since `.Wait()` is just waiting for us to cancel the in-progress context

		return
	}
	namespaces.Wait = func() error {
		<-closeInProgressContext.Done()

		return nil
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the Close func. We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	handleGoroutinePanics(false, func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-internalCtx.Done():
			if err := namespaces.Close(); err != nil {
				panic(err)
			}

		// Happy case; we've set up all of the namespaces and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-readyCtx.Done():
			<-ctx.Done()

			if err := namespaces.Close(); err != nil {
				panic(err)
			}

			break
		}
	})

	for i := uint64(0); i < availableIPs; i++ {
		id := fmt.Sprintf("%v%v", namespacePrefix, i)

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
			hostVeth, err = hostVethIPs.GetPair(internalCtx)
			if err != nil {
				if e := namespaceVethIPs.ReleasePair(closeCtx, hostVeth); e != nil {
					return errors.Join(err, e)
				}

				return err
			}

			hostVeths = append(hostVeths, hostVeth)

			return nil
		}(); err != nil {
			panic(err)
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
			namespaceVeth, err = namespaceVethIPs.GetIP(internalCtx)
			if err != nil {
				if e := namespaceVethIPs.ReleaseIP(closeCtx, namespaceVeth); e != nil {
					return errors.Join(err, e)
				}

				return err
			}

			namespaceVeths = append(namespaceVeths, namespaceVeth)

			return nil
		}(); err != nil {
			panic(err)
		}

		if err := func() error {
			claimableNamespacesLock.Lock()
			defer claimableNamespacesLock.Unlock()

			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				break
			}

			if hook := hooks.OnBeforeCreateNamespace; hook != nil {
				hook(id)
			}

			namespace := network.NewNamespace(
				id,

				hostInterface,
				namespaceInterface,

				namespaceInterfaceGateway,
				namespaceInterfaceNetmask,

				hostVeth.GetFirstIP().String(),
				hostVeth.GetSecondIP().String(),

				namespaceInterfaceIP,
				namespaceVeth.String(),

				namespaceVethCIDR,

				namespaceInterfaceMAC,
			)
			if err := namespace.Open(); err != nil {
				if e := namespace.Close(); e != nil {
					return errors.Join(err, e)
				}

				return err
			}

			claimableNamespaces[id] = claimableNamespace{
				namespace: namespace,
			}

			return nil
		}(); err != nil {
			panic(err)
		}
	}

	namespaces.ClaimNamespace = func() (string, error) {
		claimableNamespacesLock.Lock()
		defer claimableNamespacesLock.Unlock()

		for _, namespace := range claimableNamespaces {
			if !namespace.claimed {
				namespace.claimed = true

				return namespace.namespace.GetID(), nil
			}
		}

		return "", ErrAllNamespacesClaimed
	}

	namespaces.ReleaseNamespace = func(namespace string) error {
		claimableNamespacesLock.Lock()
		defer claimableNamespacesLock.Unlock()

		ns, ok := claimableNamespaces[namespace]
		if !ok {
			// Releasing non-claimed namespaces is a no-op
			return nil
		}

		ns.claimed = false

		return nil
	}

	cancelReadyCtx()

	return
}
