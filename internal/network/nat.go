package network

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/coreos/go-iptables/iptables"
)

var (
	ErrCouldNotWriteIPForwarding      = errors.New("could not enable IP forwarding")
	ErrCouldNotCreateIPTablesInstance = errors.New("could not create iptables instance")
	ErrCouldNotAppendPostRoutingRule  = errors.New("could not append POSTROUTING rule to nat table")
	ErrCouldNotAppendForwardRule      = errors.New("could not append FORWARD rule to filter table")
	ErrCouldNotDeleteForwardRule      = errors.New("could not delete FORWARD rule from filter table")
	ErrCouldNotDeletePostRoutingRule  = errors.New("could not delete POSTROUTING rule from nat table")
)

func CreateNAT(hostInterface string) error {
	if err := os.WriteFile(filepath.Join("/proc", "sys", "net", "ipv4", "ip_forward"), []byte("1"), os.ModePerm); err != nil {
		return errors.Join(ErrCouldNotWriteIPForwarding, err)
	}

	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(60),
	)
	if err != nil {
		return errors.Join(ErrCouldNotCreateIPTablesInstance, err)
	}

	if err := iptable.Append("nat", "POSTROUTING", "-o", hostInterface, "-j", "MASQUERADE"); err != nil {
		return errors.Join(ErrCouldNotAppendPostRoutingRule, err)
	}

	if err := iptable.Append("filter", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return errors.Join(ErrCouldNotAppendForwardRule, err)
	}

	return nil
}

func RemoveNAT(hostInterface string) error {
	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(60),
	)
	if err != nil {
		return errors.Join(ErrCouldNotCreateIPTablesInstance, err)
	}

	if err := iptable.Delete("filter", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return errors.Join(ErrCouldNotDeleteForwardRule, err)
	}

	if err := iptable.Delete("nat", "POSTROUTING", "-o", hostInterface, "-j", "MASQUERADE"); err != nil {
		return errors.Join(ErrCouldNotDeletePostRoutingRule, err)
	}

	return nil
}
