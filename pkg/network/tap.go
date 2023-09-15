package network

import (
	"errors"
	"fmt"
	"os/exec"
)

var (
	ErrCouldNotCreateInterface             = errors.New("could not create interface")
	ErrCouldNotAttachInterfaceToBridge     = errors.New("could not attach interface to bridge")
	ErrCouldNotAssignMACAddressToInterface = errors.New("could not assign MAC address to bridge")
	ErrCouldNotSetInterfaceUp              = errors.New("could not set interface up")
	ErrCouldNotEnableProxyARPForInterface  = errors.New("could not enable proxy ARP for interface")
	ErrCouldNotDeleteInterface             = errors.New("could not delete interface")
)

type TAP struct {
	hostInterface,
	hostMAC,
	bridgeInterface string
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
	if output, err := exec.Command("ip", "tuntap", "add", t.hostInterface, "mode", "tap").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotCreateInterface, output)
	}

	if output, err := exec.Command("ip", "link", "set", "dev", t.hostInterface, "master", t.bridgeInterface).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotAttachInterfaceToBridge, output)
	}

	if output, err := exec.Command("ip", "link", "set", "dev", t.hostInterface, "address", t.hostMAC).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotAssignMACAddressToInterface, output)
	}

	if output, err := exec.Command("ip", "link", "set", t.hostInterface, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotSetInterfaceUp, output)
	}

	if output, err := exec.Command("sysctl", "-w", "net.ipv4.conf."+t.hostInterface+".proxy_arp=1").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotEnableProxyARPForInterface, output)
	}

	return nil
}

func (t *TAP) Close() error {
	if output, err := exec.Command("ip", "tuntap", "del", "dev", t.hostInterface, "mode", "tap").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotCreateInterface, output)
	}

	return nil
}
