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

	hostIface  string
	guestIface string

	internalGateway string
	gatewayMask     uint32

	internalVeth string
	externalVeth string

	internalAddr string
	externalAddr string

	blockedSubnet string

	mac string

	veth0 string
	veth1 string

	parsedExternalAddr   *netlink.Addr
	parsedDefaultAddress *netlink.Addr
}

func NewNamespace(
	id string,

	hostIface string,
	guestIface string,

	internalGateway string,
	gatewayMask uint32,

	internalVeth string,
	externalVeth string,

	internalAddr string,
	externalAddr string,

	blockedSubnet string,

	mac string,
) *Namespace {
	return &Namespace{
		id: id,

		hostIface:  hostIface,
		guestIface: guestIface,

		internalGateway: internalGateway,
		gatewayMask:     gatewayMask,

		internalVeth: internalVeth,
		externalVeth: externalVeth,

		internalAddr: internalAddr,
		externalAddr: externalAddr,

		blockedSubnet: blockedSubnet,

		mac: mac,

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

	tapIface.Name = n.guestIface
	tapIface.Mode = netlink.TUNTAP_MODE_TAP

	if err := netlink.LinkAdd(tapIface); err != nil {
		return err
	}

	gatewaySubnet := fmt.Sprintf("%s/%d", n.internalGateway, n.gatewayMask)
	parsedGatewaySubnet, err := netlink.ParseAddr(gatewaySubnet)
	if err != nil {
		return err
	}

	if err := netlink.AddrAdd(tapIface, parsedGatewaySubnet); err != nil {
		return err
	}

	parsedMAC, err := net.ParseMAC(n.mac)
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

	internalVethSubnet := fmt.Sprintf("%s/%d", n.internalVeth, PairMask)
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

	externalVethSubnet := fmt.Sprintf("%s/%d", n.externalVeth, PairMask)
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
		Gw:  net.ParseIP(n.externalVeth),
	}); err != nil {
		return err
	}

	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(5))
	if err != nil {
		return err
	}

	if err = iptable.Append("nat", "POSTROUTING", "-o", n.veth0, "-s", n.internalAddr, "-j", "SNAT", "--to", n.externalAddr); err != nil {
		return err
	}

	if err := iptable.Append("nat", "PREROUTING", "-i", n.veth0, "-d", n.externalAddr, "-j", "DNAT", "--to", n.internalAddr); err != nil {
		return err
	}

	if err := netns.Set(originalNSHandle); err != nil {
		return err
	}

	n.parsedExternalAddr, err = netlink.ParseAddr(n.externalAddr + "/32")
	if err != nil {
		return err
	}

	if err := netlink.RouteAdd(&netlink.Route{
		Dst: n.parsedExternalAddr.IPNet,
		Gw:  net.ParseIP(n.internalVeth),
	}); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-i", n.veth1, "-o", n.hostIface, "-j", "ACCEPT"); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.externalVeth, "-j", "DROP"); err != nil {
		return err
	}

	if err := iptable.Append("filter", "FORWARD", "-s", n.externalVeth, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {
		return err
	}

	return nil
}

func (n *Namespace) Close() error {
	iptable, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(5))
	if err != nil {
		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.blockedSubnet, "-d", n.externalVeth, "-j", "DROP"); err != nil {
		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-s", n.externalVeth, "-d", n.blockedSubnet, "-j", "DROP"); err != nil {

		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-i", n.veth1, "-o", n.hostIface, "-j", "ACCEPT"); err != nil {
		return err
	}

	if n.parsedExternalAddr != nil {
		if err = netlink.RouteDel(&netlink.Route{
			Dst: n.parsedExternalAddr.IPNet,
			Gw:  net.ParseIP(n.internalVeth),
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

	if err := iptable.Delete("nat", "PREROUTING", "-i", n.veth0, "-d", n.externalAddr, "-j", "DNAT", "--to", n.internalAddr); err != nil {
		return err
	}

	if err := iptable.Delete("nat", "POSTROUTING", "-o", n.veth0, "-s", n.internalAddr, "-j", "SNAT", "--to", n.externalAddr); err != nil {
		return err
	}

	if n.parsedDefaultAddress != nil {
		if err := netlink.RouteDel(&netlink.Route{
			Dst: n.parsedDefaultAddress.IPNet,
			Gw:  net.ParseIP(n.externalVeth),
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

	tapIface, err := netlink.LinkByName(n.guestIface)
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
