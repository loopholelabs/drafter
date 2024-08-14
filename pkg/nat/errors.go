package nat

import "errors"

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
