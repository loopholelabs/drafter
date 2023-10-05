package network

import (
	"fmt"
	"net"
	"runtime"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type Namespace struct {
	id string

	hostInterface      string
	namespaceInterface string

	namespaceInterfaceGateway string
	namespaceInterfaceNetmask uint32

	hostVethInternalIP string
	hostVethExternalIP string

	namespaceInterfaceIP string
	namespaceVethIP      string

	blockedSubnet string

	namespaceInterfaceMAC string

	veth0 string
	veth1 string

	parsedExternalAddr   *netlink.Addr
	parsedDefaultAddress *netlink.Addr
}

func NewNamespace(
	id string,

	hostInterface string,
	namespaceInterface string,

	namespaceInterfaceGateway string,
	namespaceInterfaceNetmask uint32,

	hostVethInternalIP string,
	hostVethExternalIP string,

	namespaceInterfaceIP string,
	namespaceVethIP string,

	blockedSubnet string,

	namespaceInterfaceMAC string,
) *Namespace {
	return &Namespace{
		id: id,

		hostInterface:      hostInterface,
		namespaceInterface: namespaceInterface,

		namespaceInterfaceGateway: namespaceInterfaceGateway,
		namespaceInterfaceNetmask: namespaceInterfaceNetmask,

		hostVethInternalIP: hostVethInternalIP,
		hostVethExternalIP: hostVethExternalIP,

		namespaceInterfaceIP: namespaceInterfaceIP,
		namespaceVethIP:      namespaceVethIP,

		blockedSubnet: blockedSubnet,

		namespaceInterfaceMAC: namespaceInterfaceMAC,

		veth0: fmt.Sprintf("%s-veth0", id),
		veth1: fmt.Sprintf("%s-veth1", id),
	}
}

func (n *Namespace) Open() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	originalNSHandle, err := netns.Get()
	if err != nil {
		return err
	}
	defer originalNSHandle.Close()
	defer netns.Set(originalNSHandle)

	nsHandle, err := netns.NewNamed(n.id)
	if err != nil {
		return err
	}
	defer nsHandle.Close()

	tapIface := &netlink.Tuntap{
		LinkAttrs: netlink.NewLinkAttrs(),
	}

	tapIface.Name = n.namespaceInterface
	tapIface.Mode = netlink.TUNTAP_MODE_TAP

	if err := netlink.LinkAdd(tapIface); err != nil {
		return err
	}

	gatewaySubnet := fmt.Sprintf("%s/%d", n.namespaceInterfaceGateway, n.namespaceInterfaceNetmask)
	parsedGatewaySubnet, err := netlink.ParseAddr(gatewaySubnet)
	if err != nil {
		return err
	}

	if err := netlink.AddrAdd(tapIface, parsedGatewaySubnet); err != nil {
		return err
	}

	parsedMAC, err := net.ParseMAC(n.namespaceInterfaceMAC)
	if err != nil {
		return err
	}

	if err := netlink.LinkSetHardwareAddr(tapIface, parsedMAC); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(tapIface); err != nil {
		return err
	}

	veth0Iface := &netlink.Veth{
		LinkAttrs:        netlink.NewLinkAttrs(),
		PeerName:         "",
		PeerHardwareAddr: nil,
	}

	veth0Iface.Name = n.veth0
	veth0Iface.PeerName = n.veth1

	if err := netlink.LinkAdd(veth0Iface); err != nil {
		return err
	}

	veth1Iface, err := netlink.LinkByName(n.veth1)
	if err != nil {
		return err
	}

	if err := netlink.LinkSetNsPid(veth1Iface, 1); err != nil {
		return err
	}

	internalVethSubnet := fmt.Sprintf("%s/%d", n.hostVethInternalIP, PairMask)
	parsedInternalVethSubnet, err := netlink.ParseAddr(internalVethSubnet)
	if err != nil {
		return err
	}

	if err := netlink.AddrAdd(veth0Iface, parsedInternalVethSubnet); err != nil {
		return err
	}

	if err = netlink.LinkSetUp(veth0Iface); err != nil {
		return err
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return err
	}

	externalVethSubnet := fmt.Sprintf("%s/%d", n.hostVethExternalIP, PairMask)
	parsedExternalVethSubnet, err := netlink.ParseAddr(externalVethSubnet)
	if err != nil {
		return err
	}

	veth1Iface, err = netlink.LinkByName(n.veth1)
	if err != nil {
		return err
	}

	if err := netlink.AddrAdd(veth1Iface, parsedExternalVethSubnet); err != nil {
		return err
	}

	if err = netlink.LinkSetUp(veth1Iface); err != nil {
		return err
	}

	if err := netns.Set(nsHandle); err != nil {
		return err
	}

	n.parsedDefaultAddress, err = netlink.ParseAddr("0.0.0.0/0")
	if err != nil {
		return err
	}

	if err := netlink.RouteAdd(&netlink.Route{
		Dst: n.parsedDefaultAddress.IPNet,
		Gw:  net.ParseIP(n.hostVethExternalIP),
	}); err != nil {
		return err
	}

	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(5))
	if err != nil {
		return err
	}

	if err = iptable.Append("nat", "POSTROUTING", "-o", n.veth0, "-s", n.namespaceInterfaceIP, "-j", "SNAT", "--to", n.namespaceVethIP); err != nil {
		return err
	}

	if err := iptable.Append("nat", "PREROUTING", "-i", n.veth0, "-d", n.namespaceVethIP, "-j", "DNAT", "--to", n.namespaceInterfaceIP); err != nil {
		return err
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return err
	}

	n.parsedExternalAddr, err = netlink.ParseAddr(n.namespaceVethIP + "/32")
	if err != nil {
		return err
	}

	if err := netlink.RouteAdd(&netlink.Route{
		Dst: n.parsedExternalAddr.IPNet,
		Gw:  net.ParseIP(n.hostVethInternalIP),
	}); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-i", n.veth1, "-o", n.hostInterface, "-j", "ACCEPT"); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.hostVethExternalIP, "-j", "DROP"); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.hostVethExternalIP, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {
		return err
	}

	return nil
}

func (n *Namespace) Close() error {
	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(5))
	if err != nil {
		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.hostVethExternalIP, "-j", "DROP"); err != nil {
		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.hostVethExternalIP, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {

		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-i", n.veth1, "-o", n.hostInterface, "-j", "ACCEPT"); err != nil {
		return err
	}

	if n.parsedExternalAddr != nil {
		if err = netlink.RouteDel(&netlink.Route{
			Dst: n.parsedExternalAddr.IPNet,
			Gw:  net.ParseIP(n.hostVethInternalIP),
		}); err != nil {
			return err
		}
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	originalNSHandle, err := netns.Get()
	if err != nil {
		return err
	}
	defer originalNSHandle.Close()
	defer netns.Set(originalNSHandle)

	nsHandle, err := netns.GetFromName(n.id)
	if err != nil {
		return err
	}
	defer nsHandle.Close()

	if err = netns.Set(nsHandle); err != nil {
		return err
	}

	if err := iptable.Delete("nat", "PREROUTING", "-i", n.veth0, "-d", n.namespaceVethIP, "-j", "DNAT", "--to", n.namespaceInterfaceIP); err != nil {
		return err
	}

	if err := iptable.Delete("nat", "POSTROUTING", "-o", n.veth0, "-s", n.namespaceInterfaceIP, "-j", "SNAT", "--to", n.namespaceVethIP); err != nil {
		return err
	}

	if n.parsedDefaultAddress != nil {
		if err := netlink.RouteDel(&netlink.Route{
			Dst: n.parsedDefaultAddress.IPNet,
			Gw:  net.ParseIP(n.hostVethExternalIP),
		}); err != nil {
			return err
		}
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return err
	}

	veth1Iface, err := netlink.LinkByName(n.veth1)
	if err != nil {
		return err
	}

	if err := netlink.LinkDel(veth1Iface); err != nil {
		return err
	}

	if err := netns.Set(nsHandle); err != nil {
		return err
	}

	tapIface, err := netlink.LinkByName(n.namespaceInterface)
	if err != nil {
		return err
	}

	if err = netlink.LinkSetDown(tapIface); err != nil {
		return err
	}

	if err := netlink.LinkDel(tapIface); err != nil {
		return err
	}

	return netns.DeleteNamed(n.id)
}

func (n *Namespace) GetID() string {
	return n.id
}
