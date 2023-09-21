package network

import (
	"errors"
	"fmt"
	"net"
	"os/exec"

	"github.com/vishvananda/netlink"
)

var (
	ErrCouldNotCreateInterface             = errors.New("could not create interface")
	ErrCouldNotAttachInterfaceToBridge     = errors.New("could not attach interface to bridge")
	ErrCouldNotAssignMACAddressToInterface = errors.New("could not assign MAC address to bridge")
	ErrCouldNotSetInterfaceUp              = errors.New("could not set interface up")
	ErrCouldNotEnableProxyARPForInterface  = errors.New("could not enable proxy ARP for interface")
	ErrCouldNotDeleteInterface             = errors.New("could not delete interface")
	ErrCouldNotFindBridge                  = errors.New("could not find bridge")
)

type TAP struct {
	hostInterface,
	hostMAC,
	bridgeInterface string
	link netlink.Link
}

func NewTAP(
	hostInterface,
	hostMAC,
	bridgeInterface string,
) *TAP {
	return &TAP{
		hostInterface:   hostInterface,
		hostMAC:         hostMAC,
		bridgeInterface: bridgeInterface,
	}
}

func (t *TAP) Open() error {
	la := netlink.NewLinkAttrs()
	la.Name = t.hostInterface

	tapLink := &netlink.Tuntap{
		LinkAttrs: la,
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tapLink); err != nil {
		return fmt.Errorf("%w: %v", ErrCouldNotCreateInterface, err)
	}
	t.link = tapLink

	bridge, err := netlink.LinkByName(t.bridgeInterface)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCouldNotFindBridge, err)
	}

	if err := netlink.LinkSetMaster(tapLink, bridge); err != nil {
		return fmt.Errorf("%w: %v", ErrCouldNotAttachInterfaceToBridge, err)
	}

	mac, err := net.ParseMAC(t.hostMAC)
	if err != nil {
		return err
	}

	if err := netlink.LinkSetHardwareAddr(tapLink, mac); err != nil {
		return fmt.Errorf("%w: %v", ErrCouldNotAssignMACAddressToInterface, err)
	}

	if err := netlink.LinkSetUp(tapLink); err != nil {
		return fmt.Errorf("%w: %v", ErrCouldNotSetInterfaceUp, err)
	}

	if output, err := exec.Command("sysctl", "-w", "net.ipv4.conf."+t.hostInterface+".proxy_arp=1").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotEnableProxyARPForInterface, output)
	}

	return nil
}

func (t *TAP) Close() error {
	if t.link != nil {
		if err := netlink.LinkDel(t.link); err != nil {
			return fmt.Errorf("%w: %v", ErrCouldNotDeleteInterface, err)
		}
	}

	return nil
}
