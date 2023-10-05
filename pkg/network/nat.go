package network

import (
	"os"

	"github.com/coreos/go-iptables/iptables"
)

func CreateNAT(hostInterface string) error {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), os.ModePerm); err != nil {
		return err
	}

	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(5),
	)
	if err != nil {
		return err
	}

	if err := iptable.Append("nat", "POSTROUTING", "-o", hostInterface, "-j", "MASQUERADE"); err != nil {
		return err
	}

	return iptable.Append("filter", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
}

func RemoveNAT(hostInterface string) error {
	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(5),
	)
	if err != nil {
		return err
	}

	if err := iptable.Delete("filter", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return err
	}

	return iptable.Delete("nat", "POSTROUTING", "-o", hostInterface, "-j", "MASQUERADE")
}
