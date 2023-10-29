package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

var (
	ErrNotEnoughAvailableIPsInHostCIDR      = errors.New("not enough available IPs in host CIDR")
	ErrNotEnoughAvailableIPsInNamespaceCIDR = errors.New("not enough available IPs in namespace CIDR")
	ErrAllNamespacesClaimed                 = errors.New("all namespaces claimed")
)

type claimableNamespace struct {
	namespace *Namespace
	claimed   bool
}

type Daemon struct {
	hostInterface string

	hostVethCIDR      string
	namespaceVethCIDR string

	namespaceInterface        string
	namespaceInterfaceGateway string
	namespaceInterfaceNetmask uint32
	namespaceInterfaceIP      string
	namespaceInterfaceMAC     string

	namespacePrefix string

	onBeforeCreateNamespace func(id string) error
	onBeforeRemoveNamespace func(id string) error

	namespaceVeths     []*IP
	namespaceVethsLock sync.Mutex

	namespaces     map[string]claimableNamespace
	namespacesLock sync.Mutex

	namespaceVethIPs *IPTable
}

func NewDaemon(
	hostInterface string,

	hostVethCIDR string,
	namespaceVethCIDR string,

	namespaceInterface string,
	namespaceInterfaceGateway string,
	namespaceInterfaceNetmask uint32,
	namespaceInterfaceIP string,
	namespaceInterfaceMAC string,

	namespacePrefix string,

	onBeforeCreateNamespace func(id string) error,
	onBeforeRemoveNamespace func(id string) error,
) *Daemon {
	return &Daemon{
		hostInterface: hostInterface,

		hostVethCIDR:      hostVethCIDR,
		namespaceVethCIDR: namespaceVethCIDR,

		namespaceInterface:        namespaceInterface,
		namespaceInterfaceGateway: namespaceInterfaceGateway,
		namespaceInterfaceNetmask: namespaceInterfaceNetmask,
		namespaceInterfaceIP:      namespaceInterfaceIP,
		namespaceInterfaceMAC:     namespaceInterfaceMAC,

		namespacePrefix: namespacePrefix,

		onBeforeCreateNamespace: onBeforeCreateNamespace,
		onBeforeRemoveNamespace: onBeforeRemoveNamespace,

		namespaceVeths:     []*IP{},
		namespaceVethsLock: sync.Mutex{},

		namespaces:     map[string]claimableNamespace{},
		namespacesLock: sync.Mutex{},

		namespaceVethIPs: NewIPTable(namespaceVethCIDR),
	}
}

func (d *Daemon) Open(ctx context.Context) error {
	// Check if the host interface exists
	if _, err := net.InterfaceByName(d.hostInterface); err != nil {
		return err
	}

	if err := CreateNAT(d.hostInterface); err != nil {
		return err
	}

	hostVethIPs := NewIPTable(d.hostVethCIDR)
	if err := hostVethIPs.Open(ctx); err != nil {
		return err
	}

	if err := d.namespaceVethIPs.Open(ctx); err != nil {
		return err
	}

	if d.namespaceVethIPs.AvailableIPs() > hostVethIPs.AvailablePairs() {
		return ErrNotEnoughAvailableIPsInHostCIDR
	}

	availableIPs := d.namespaceVethIPs.AvailableIPs()
	if availableIPs < 1 {
		return ErrNotEnoughAvailableIPsInNamespaceCIDR
	}

	var (
		setupWg sync.WaitGroup
		errs    = make(chan error)
	)
	for i := uint64(0); i < availableIPs; i++ {
		setupWg.Add(1)

		go func(i uint64) {
			defer setupWg.Done()

			id := fmt.Sprintf("%v%v", d.namespacePrefix, i)

			hostVeth, err := hostVethIPs.GetPair(ctx)
			if err != nil {
				errs <- err

				return
			}

			namespaceVeth, err := d.namespaceVethIPs.GetIP(ctx)
			if err != nil {
				errs <- err

				return
			}

			d.namespaceVethsLock.Lock()
			d.namespaceVeths = append(d.namespaceVeths, namespaceVeth)
			d.namespaceVethsLock.Unlock()

			namespace := NewNamespace(
				id,

				d.hostInterface,
				d.namespaceInterface,

				d.namespaceInterfaceGateway,
				d.namespaceInterfaceNetmask,

				hostVeth.GetFirstIP().String(),
				hostVeth.GetSecondIP().String(),

				d.namespaceInterfaceIP,
				namespaceVeth.String(),

				d.namespaceVethCIDR,

				d.namespaceInterfaceMAC,
			)
			if err := namespace.Open(); err != nil {
				errs <- err

				return
			}

			d.namespacesLock.Lock()
			d.namespaces[id] = claimableNamespace{
				namespace: namespace,
			}
			d.namespacesLock.Unlock()

			if hook := d.onBeforeCreateNamespace; hook != nil {
				if err := hook(id); err != nil {
					errs <- err

					return
				}
			}
		}(i)
	}

	go func() {
		setupWg.Wait()

		close(errs)
	}()

	for err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *Daemon) Close(ctx context.Context) error {
	d.namespaceVethsLock.Lock()
	defer d.namespaceVethsLock.Unlock()

	for _, namespaceVeth := range d.namespaceVeths {
		if err := d.namespaceVethIPs.ReleaseIP(ctx, namespaceVeth); err != nil {
			return err
		}
	}

	d.namespacesLock.Lock()
	defer d.namespacesLock.Unlock()

	var (
		teardownWg sync.WaitGroup
		errs       = make(chan error)
	)
	for _, namespace := range d.namespaces {
		teardownWg.Add(1)

		go func(ns *Namespace) {
			defer teardownWg.Done()

			if err := ns.Close(); err != nil {
				errs <- err

				return
			}

			if hook := d.onBeforeRemoveNamespace; hook != nil {
				if err := hook(ns.GetID()); err != nil {
					errs <- err

					return
				}
			}
		}(namespace.namespace)
	}

	go func() {
		teardownWg.Wait()

		close(errs)
	}()

	for err := range errs {
		if err != nil {
			return err
		}
	}

	return RemoveNAT(d.hostInterface)
}

func (d *Daemon) ClaimNamespace() (string, error) {
	d.namespacesLock.Lock()
	defer d.namespacesLock.Unlock()

	for _, namespace := range d.namespaces {
		if !namespace.claimed {
			namespace.claimed = true

			return namespace.namespace.id, nil
		}
	}

	return "", ErrAllNamespacesClaimed
}

func (d *Daemon) ReleaseNamespace(namespace string) error {
	d.namespacesLock.Lock()
	defer d.namespacesLock.Unlock()

	ns, ok := d.namespaces[namespace]
	if !ok {
		// Releasing non-claimed namespaces is a no-op
		return nil
	}

	ns.claimed = false

	return nil
}
