package roles

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/loopholelabs/drafter/internal/network"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

var (
	ErrNotEnoughAvailableIPsInHostCIDR      = errors.New("not enough available IPs in host CIDR")
	ErrNotEnoughAvailableIPsInNamespaceCIDR = errors.New("not enough available IPs in namespace CIDR")
	ErrAllNamespacesClaimed                 = errors.New("all namespaces claimed")
	ErrCouldNotFindHostInterface            = errors.New("could not find host interface")
	ErrCouldNotCreateNAT                    = errors.New("could not create NAT")
	ErrCouldNotOpenHostVethIPs              = errors.New("could not open host Veth IPs")
	ErrCouldNotOpenNamespaceVethIPs         = errors.New("could not open namespace Veth IPs")
	ErrCouldNotReleaseHostVethIP            = errors.New("could not release host Veth IP")
	ErrCouldNotReleaseNamespaceVethIP       = errors.New("could not release namespace Veth IP")
	ErrCouldNotOpenNamespace                = errors.New("could not open namespace")
	ErrCouldNotCloseNamespace               = errors.New("could not close namespace")
	ErrCouldNotRemoveNAT                    = errors.New("could not remove NAT")
	ErrNATContextCancelled                  = errors.New("context for NAT cancelled")
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

func CreateNAT(
	ctx context.Context,
	closeCtx context.Context,

	hostInterface string,

	hostVethCIDR string,
	namespaceVethCIDR string,
	blockedSubnetCIDR string,

	namespaceInterface string,
	namespaceInterfaceGateway string,
	namespaceInterfaceNetmask uint32,
	namespaceInterfaceIP string,
	namespaceInterfaceMAC string,

	namespacePrefix string,

	allowIncomingTraffic bool,

	hooks CreateNamespacesHooks,
) (namespaces *Namespaces, errs error) {
	namespaces = &Namespaces{
		Wait:  func() error { return nil },
		Close: func() error { return nil },

		claimableNamespaces: map[string]*claimableNamespace{},
	}

	// We use the background context here instead of the internal context because we want to distinguish
	// between a context cancellation from the outside and getting a response
	readyCtx, cancelReadyCtx := context.WithCancel(context.Background())
	defer cancelReadyCtx()

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.WaitForForegroundGoroutines()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	// Check if the host interface exists
	if _, err := net.InterfaceByName(hostInterface); err != nil {
		panic(errors.Join(ErrCouldNotFindHostInterface, err))
	}

	if err := network.CreateNAT(hostInterface); err != nil {
		panic(errors.Join(ErrCouldNotCreateNAT, err))
	}

	hostVethIPs := network.NewIPTable(hostVethCIDR, goroutineManager.GetGoroutineCtx())
	if err := hostVethIPs.Open(goroutineManager.GetGoroutineCtx()); err != nil {
		panic(errors.Join(ErrCouldNotOpenHostVethIPs, err))
	}

	namespaceVethIPs := network.NewIPTable(namespaceVethCIDR, goroutineManager.GetGoroutineCtx())
	if err := namespaceVethIPs.Open(goroutineManager.GetGoroutineCtx()); err != nil {
		panic(errors.Join(ErrCouldNotOpenNamespaceVethIPs, err))
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
				errs = errors.Join(errs, ErrCouldNotReleaseHostVethIP, err)
			}
		}

		hostVeths = []*network.IPPair{}

		namespaceVethsLock.Lock()
		defer namespaceVethsLock.Unlock()

		for _, namespaceVeth := range namespaceVeths {
			if err := namespaceVethIPs.ReleaseIP(closeCtx, namespaceVeth); err != nil {
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

			if err := network.RemoveNAT(hostInterface); err != nil {
				errs = errors.Join(errs, ErrCouldNotRemoveNAT, err)
			}
		}

		// No need to call `.Wait()` here since `.Wait()` is just waiting for us to cancel the in-progress context

		return
	}
	// Future-proofing; if we decide that NATing should use a background copy loop like `socat`, we can wait for that loop to finish here and return any errors
	namespaces.Wait = func() error {
		<-closeInProgressContext.Done()

		return nil
	}

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the Close func. We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	goroutineManager.StartBackgroundGoroutine(func() {
		select {
		// Failure case; we cancelled the internal context before we got a connection
		case <-goroutineManager.GetGoroutineCtx().Done():
			if err := namespaces.Close(); err != nil {
				panic(errors.Join(ErrNATContextCancelled, err))
			}

		// Happy case; we've set up all of the namespaces and we want to wait with closing the agent's connections until the context, not the internal context is cancelled
		case <-readyCtx.Done():
			<-ctx.Done()

			if err := namespaces.Close(); err != nil {
				panic(errors.Join(ErrNATContextCancelled, err))
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
			hostVeth, err = hostVethIPs.GetPair(goroutineManager.GetGoroutineCtx())
			if err != nil {
				if e := namespaceVethIPs.ReleasePair(closeCtx, hostVeth); e != nil {
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
			namespaceVeth, err = namespaceVethIPs.GetIP(goroutineManager.GetGoroutineCtx())
			if err != nil {
				if e := namespaceVethIPs.ReleaseIP(closeCtx, namespaceVeth); e != nil {
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
			namespaces.claimableNamespacesLock.Lock()
			defer namespaces.claimableNamespacesLock.Unlock()

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

				blockedSubnetCIDR,

				namespaceInterfaceMAC,

				allowIncomingTraffic,
			)
			if err := namespace.Open(); err != nil {
				if e := namespace.Close(); e != nil {
					return errors.Join(ErrCouldNotOpenNamespace, err, e)
				}

				return err
			}

			namespaces.claimableNamespaces[id] = &claimableNamespace{
				namespace: namespace,
			}

			return nil
		}(); err != nil {
			panic(errors.Join(ErrCouldNotOpenNamespace, err))
		}
	}

	cancelReadyCtx()

	return
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
