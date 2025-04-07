package forwarder

import "errors"

var (
	ErrCouldNotFindInternalHostVeth         = errors.New("could not find internal host veth")
	ErrCouldNotCreatingIPTables             = errors.New("could not create iptables")
	ErrCouldNotSplitHostPort                = errors.New("could not split host and port")
	ErrCouldNotResolveIPAddr                = errors.New("could not resolve IP address")
	ErrCouldNotWriteFileRouteLocalnetAll    = errors.New("could not write to /proc/sys/net/ipv4/conf/all/route_localnet")
	ErrCouldNotWriteFileRouteLocalnetLo     = errors.New("could not write to /proc/sys/net/ipv4/conf/lo/route_localnet")
	ErrCouldNotGetOriginalNSHandle          = errors.New("could not get original namespace handle")
	ErrCouldNotSetOriginalNSHandle          = errors.New("could not set original namespace handle")
	ErrCouldNotGetNSHandle                  = errors.New("could not get namespace handle")
	ErrCouldNotSetNSHandle                  = errors.New("could not set namespace handle")
	ErrCouldNotListInterfaces               = errors.New("could not list interfaces")
	ErrCouldNotGetInterfaceAddresses        = errors.New("could not get interface addresses")
	ErrCouldNotAppendIPTablesFilterForwardD = errors.New("could not append iptables filter FORWARD -d rule")
	ErrCouldNotDeleteIPTablesFilterForwardD = errors.New("could not delete iptables filter FORWARD -d rule")
	ErrCouldNotAppendIPTablesFilterForwardS = errors.New("could not append iptables filter FORWARD -s rule")
	ErrCouldNotDeleteIPTablesFilterForwardS = errors.New("could not delete iptables filter FORWARD -s rule")
	ErrCouldNotAppendIPTablesNatOutput      = errors.New("could not append iptables nat OUTPUT rule")
	ErrCouldNotDeleteIPTablesNatOutput      = errors.New("could not delete iptables nat OUTPUT rule")
	ErrCouldNotAppendIPTablesNatPrerouting  = errors.New("could not append iptables nat PREROUTING rule")
	ErrCouldNotDeleteIPTablesNatPrerouting  = errors.New("could not delete iptables nat PREROUTING rule")
	ErrCouldNotAppendIPTablesNatPostrouting = errors.New("could not append iptables nat POSTROUTING rule")
	ErrCouldNotDeleteIPTablesNatPostrouting = errors.New("could not delete iptables nat POSTROUTING rule")
)
