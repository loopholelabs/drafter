package network

import (
	"errors"
	"fmt"
	"net"
	"runtime"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

var (
	ErrCouldNotGetOriginalNamespace    = errors.New("could not get original namespace")
	ErrCouldNotNewNamespace            = errors.New("could not create new namespace")
	ErrCouldNotAddTapInterface         = errors.New("could not add tap interface")
	ErrCouldNotParseGatewaySubnet      = errors.New("could not parse gateway subnet")
	ErrCouldNotAddAddressToInterface   = errors.New("could not add address to interface")
	ErrCouldNotParseMACAddress         = errors.New("could not parse MAC address")
	ErrCouldNotSetHardwareAddress      = errors.New("could not set hardware address")
	ErrCouldNotSetLinkUp               = errors.New("could not set link up")
	ErrCouldNotAddVethInterface        = errors.New("could not add veth interface")
	ErrCouldNotGetVethInterface        = errors.New("could not get veth interface")
	ErrCouldNotSetNamespacePid         = errors.New("could not set namespace pid")
	ErrCouldNotParseInternalVethSubnet = errors.New("could not parse internal veth subnet")
	ErrCouldNotSetOriginalNamespace    = errors.New("could not set original namespace")
	ErrCouldNotParseExternalVethSubnet = errors.New("could not parse external veth subnet")
	ErrCouldNotParseDefaultAddress     = errors.New("could not parse default address")
	ErrCouldNotAddRoute                = errors.New("could not add route")
	ErrCouldNotNewIPTable              = errors.New("could not create new iptables instance")
	ErrCouldNotAppendIPTableRule       = errors.New("could not append iptables rule")
	ErrCouldNotDeleteIPTableRule       = errors.New("could not delete iptables rule")
	ErrCouldNotDeleteRoute             = errors.New("could not delete route")
	ErrCouldNotDeleteLink              = errors.New("could not delete link")
	ErrCouldNotSetLinkDown             = errors.New("could not set link down")
	ErrCouldNotDeleteNamespace         = errors.New("could not delete namespace")
)

type Namespace struct {
	id                        string
	hostInterface             string
	namespaceInterface        string
	namespaceInterfaceGateway string
	namespaceInterfaceNetmask uint32
	hostVethInternalIP        string
	hostVethExternalIP        string
	namespaceInterfaceIP      string
	namespaceVethIP           string
	blockedSubnet             string
	namespaceInterfaceMAC     string
	veth0                     string
	veth1                     string
	parsedExternalAddr        *netlink.Addr
	parsedDefaultAddress      *netlink.Addr
	allowIncomingTraffic      bool
}

type NamespaceConfig struct {
	HostInterface             string
	NamespaceInterface        string
	NamespaceInterfaceGateway string
	NamespaceInterfaceNetmask uint32
	HostVethInternalIP        string
	HostVethExternalIP        string
	NamespaceInterfaceIP      string
	NamespaceVethIP           string
	BlockedSubnet             string
	NamespaceInterfaceMAC     string
	AllowIncomingTraffic      bool
}

func NewNamespace(id string, conf *NamespaceConfig) *Namespace {
	return &Namespace{
		id:                        id,
		hostInterface:             conf.HostInterface,
		namespaceInterface:        conf.NamespaceInterface,
		namespaceInterfaceGateway: conf.NamespaceInterfaceGateway,
		namespaceInterfaceNetmask: conf.NamespaceInterfaceNetmask,
		hostVethInternalIP:        conf.HostVethInternalIP,
		hostVethExternalIP:        conf.HostVethExternalIP,
		namespaceInterfaceIP:      conf.NamespaceInterfaceIP,
		namespaceVethIP:           conf.NamespaceVethIP,
		blockedSubnet:             conf.BlockedSubnet,
		namespaceInterfaceMAC:     conf.NamespaceInterfaceMAC,
		veth0:                     fmt.Sprintf("%s-veth0", id),
		veth1:                     fmt.Sprintf("%s-veth1", id),
		allowIncomingTraffic:      conf.AllowIncomingTraffic,
	}
}

func (n *Namespace) Open() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	originalNSHandle, err := netns.Get()
	if err != nil {
		return errors.Join(ErrCouldNotGetOriginalNamespace, err)
	}
	defer originalNSHandle.Close()
	defer netns.Set(originalNSHandle)

	nsHandle, err := netns.NewNamed(n.id)
	if err != nil {
		return errors.Join(ErrCouldNotNewNamespace, err)
	}
	defer nsHandle.Close()

	tapIface := &netlink.Tuntap{
		LinkAttrs: netlink.NewLinkAttrs(),
	}

	tapIface.Name = n.namespaceInterface
	tapIface.Mode = netlink.TUNTAP_MODE_TAP

	if err := netlink.LinkAdd(tapIface); err != nil {
		return errors.Join(ErrCouldNotAddTapInterface, err)
	}

	gatewaySubnet := fmt.Sprintf("%s/%d", n.namespaceInterfaceGateway, n.namespaceInterfaceNetmask)
	parsedGatewaySubnet, err := netlink.ParseAddr(gatewaySubnet)
	if err != nil {
		return errors.Join(ErrCouldNotParseGatewaySubnet, err)
	}

	if err := netlink.AddrAdd(tapIface, parsedGatewaySubnet); err != nil {
		return errors.Join(ErrCouldNotAddAddressToInterface, err)
	}

	parsedMAC, err := net.ParseMAC(n.namespaceInterfaceMAC)
	if err != nil {
		return errors.Join(ErrCouldNotParseMACAddress, err)
	}

	if err := netlink.LinkSetHardwareAddr(tapIface, parsedMAC); err != nil {
		return errors.Join(ErrCouldNotSetHardwareAddress, err)
	}

	if err := netlink.LinkSetUp(tapIface); err != nil {
		return errors.Join(ErrCouldNotSetLinkUp, err)
	}

	veth0Iface := &netlink.Veth{
		LinkAttrs:        netlink.NewLinkAttrs(),
		PeerName:         "",
		PeerHardwareAddr: nil,
	}

	veth0Iface.Name = n.veth0
	veth0Iface.PeerName = n.veth1

	if err := netlink.LinkAdd(veth0Iface); err != nil {
		return errors.Join(ErrCouldNotAddVethInterface, err)
	}

	veth1Iface, err := netlink.LinkByName(n.veth1)
	if err != nil {
		return errors.Join(ErrCouldNotGetVethInterface, err)
	}

	if err := netlink.LinkSetNsPid(veth1Iface, 1); err != nil {
		return errors.Join(ErrCouldNotSetNamespacePid, err)
	}

	internalVethSubnet := fmt.Sprintf("%s/%d", n.hostVethInternalIP, PairMask)
	parsedInternalVethSubnet, err := netlink.ParseAddr(internalVethSubnet)
	if err != nil {
		return errors.Join(ErrCouldNotParseInternalVethSubnet, err)
	}

	if err := netlink.AddrAdd(veth0Iface, parsedInternalVethSubnet); err != nil {
		return errors.Join(ErrCouldNotAddAddressToInterface, err)
	}

	if err = netlink.LinkSetUp(veth0Iface); err != nil {
		return errors.Join(ErrCouldNotSetLinkUp, err)
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	externalVethSubnet := fmt.Sprintf("%s/%d", n.hostVethExternalIP, PairMask)
	parsedExternalVethSubnet, err := netlink.ParseAddr(externalVethSubnet)
	if err != nil {
		return errors.Join(ErrCouldNotParseExternalVethSubnet, err)
	}

	veth1Iface, err = netlink.LinkByName(n.veth1)
	if err != nil {
		return errors.Join(ErrCouldNotGetVethInterface, err)
	}

	if err := netlink.AddrAdd(veth1Iface, parsedExternalVethSubnet); err != nil {
		return errors.Join(ErrCouldNotAddAddressToInterface, err)
	}

	if err = netlink.LinkSetUp(veth1Iface); err != nil {
		return errors.Join(ErrCouldNotSetLinkUp, err)
	}

	if err := netns.Set(nsHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	n.parsedDefaultAddress, err = netlink.ParseAddr("0.0.0.0/0")
	if err != nil {
		return errors.Join(ErrCouldNotParseDefaultAddress, err)
	}

	if err := netlink.RouteAdd(&netlink.Route{
		Dst: n.parsedDefaultAddress.IPNet,
		Gw:  net.ParseIP(n.hostVethExternalIP),
	}); err != nil {
		return errors.Join(ErrCouldNotAddRoute, err)
	}

	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(60))
	if err != nil {
		return errors.Join(ErrCouldNotNewIPTable, err)
	}

	if err = iptable.Append("nat", "POSTROUTING", "-o", n.veth0, "-s", n.namespaceInterfaceIP, "-j", "SNAT", "--to", n.namespaceVethIP); err != nil {
		return errors.Join(ErrCouldNotAppendIPTableRule, err)
	}

	if err := iptable.Append("nat", "PREROUTING", "-i", n.veth0, "-d", n.namespaceVethIP, "-j", "DNAT", "--to", n.namespaceInterfaceIP); err != nil {
		return errors.Join(ErrCouldNotAppendIPTableRule, err)
	}

	if n.allowIncomingTraffic {
		if err := iptable.Append("nat", "PREROUTING", "-d", n.hostVethInternalIP, "-j", "DNAT", "--to-destination", n.namespaceInterfaceIP); err != nil {
			return errors.Join(ErrCouldNotAppendIPTableRule, err)
		}

		if err := iptable.Append("nat", "POSTROUTING", "-d", n.namespaceInterfaceIP, "-j", "MASQUERADE"); err != nil {
			return errors.Join(ErrCouldNotAppendIPTableRule, err)
		}
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	n.parsedExternalAddr, err = netlink.ParseAddr(n.namespaceVethIP + "/32")
	if err != nil {
		return errors.Join(ErrCouldNotParseExternalVethSubnet, err)
	}

	if err := netlink.RouteAdd(&netlink.Route{
		Dst: n.parsedExternalAddr.IPNet,
		Gw:  net.ParseIP(n.hostVethInternalIP),
	}); err != nil {
		return errors.Join(ErrCouldNotAddRoute, err)
	}

	if err := iptable.Append("filter", "FORWARD", "-i", n.veth1, "-o", n.hostInterface, "-j", "ACCEPT"); err != nil {
		return errors.Join(ErrCouldNotAppendIPTableRule, err)
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.hostVethExternalIP, "-j", "DROP"); err != nil {
		return errors.Join(ErrCouldNotAppendIPTableRule, err)
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.hostVethExternalIP, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {
		return errors.Join(ErrCouldNotAppendIPTableRule, err)
	}

	return nil
}

func (n *Namespace) Close() error {
	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(60))
	if err != nil {
		return errors.Join(ErrCouldNotNewIPTable, err)
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.hostVethExternalIP, "-j", "DROP"); err != nil {
		return errors.Join(ErrCouldNotDeleteIPTableRule, err)
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.hostVethExternalIP, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {
		return errors.Join(ErrCouldNotDeleteIPTableRule, err)
	}

	if err := iptable.Delete("filter", "FORWARD", "-i", n.veth1, "-o", n.hostInterface, "-j", "ACCEPT"); err != nil {
		return errors.Join(ErrCouldNotDeleteIPTableRule, err)
	}

	if n.parsedExternalAddr != nil {
		if err = netlink.RouteDel(&netlink.Route{
			Dst: n.parsedExternalAddr.IPNet,
			Gw:  net.ParseIP(n.hostVethInternalIP),
		}); err != nil {
			return errors.Join(ErrCouldNotDeleteRoute, err)
		}
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	originalNSHandle, err := netns.Get()
	if err != nil {
		return errors.Join(ErrCouldNotGetOriginalNamespace, err)
	}
	defer originalNSHandle.Close()
	defer netns.Set(originalNSHandle)

	nsHandle, err := netns.GetFromName(n.id)
	if err != nil {
		return errors.Join(ErrCouldNotNewNamespace, err)
	}
	defer nsHandle.Close()

	if err = netns.Set(nsHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	if n.allowIncomingTraffic {
		if err := iptable.Append("nat", "PREROUTING", "-d", n.hostVethInternalIP, "-j", "DNAT", "--to-destination", n.namespaceInterfaceIP); err != nil {
			return errors.Join(ErrCouldNotAppendIPTableRule, err)
		}

		if err := iptable.Append("nat", "POSTROUTING", "-d", n.namespaceInterfaceIP, "-j", "MASQUERADE"); err != nil {
			return errors.Join(ErrCouldNotAppendIPTableRule, err)
		}
	}

	if err := iptable.Delete("nat", "PREROUTING", "-i", n.veth0, "-d", n.namespaceVethIP, "-j", "DNAT", "--to", n.namespaceInterfaceIP); err != nil {
		return errors.Join(ErrCouldNotDeleteIPTableRule, err)
	}

	if err := iptable.Delete("nat", "POSTROUTING", "-o", n.veth0, "-s", n.namespaceInterfaceIP, "-j", "SNAT", "--to", n.namespaceVethIP); err != nil {
		return errors.Join(ErrCouldNotDeleteIPTableRule, err)
	}

	if n.parsedDefaultAddress != nil {
		if err := netlink.RouteDel(&netlink.Route{
			Dst: n.parsedDefaultAddress.IPNet,
			Gw:  net.ParseIP(n.hostVethExternalIP),
		}); err != nil {
			return errors.Join(ErrCouldNotDeleteRoute, err)
		}
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	veth1Iface, err := netlink.LinkByName(n.veth1)
	if err != nil {
		return errors.Join(ErrCouldNotGetVethInterface, err)
	}

	if err := netlink.LinkDel(veth1Iface); err != nil {
		return errors.Join(ErrCouldNotDeleteLink, err)
	}

	if err := netns.Set(nsHandle); err != nil {
		return errors.Join(ErrCouldNotSetOriginalNamespace, err)
	}

	tapIface, err := netlink.LinkByName(n.namespaceInterface)
	if err != nil {
		return errors.Join(ErrCouldNotAddTapInterface, err)
	}

	if err = netlink.LinkSetDown(tapIface); err != nil {
		return errors.Join(ErrCouldNotSetLinkDown, err)
	}

	if err := netlink.LinkDel(tapIface); err != nil {
		return errors.Join(ErrCouldNotDeleteLink, err)
	}

	return netns.DeleteNamed(n.id)
}

func (n *Namespace) GetID() string {
	return n.id
}
