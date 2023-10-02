package network

import (
	"context"
	"errors"
	"net"

	"github.com/metal-stack/go-ipam"
)

const (
	PairMask        = 30
	GatewayReserved = 2
)

var (
	errInvalidCIDRSize = errors.New("invalid cidr size")
)

type IPTable struct {
	cidr string

	ipam   ipam.Ipamer
	prefix *ipam.Prefix
}

func NewIPTable(cidr string) *IPTable {
	return &IPTable{
		cidr: cidr,

		ipam: ipam.New(),
	}
}

func (t *IPTable) Open(ctx context.Context) error {
	_, netCIDR, err := net.ParseCIDR(t.cidr)
	if err != nil {
		return err
	}

	if size, _ := netCIDR.Mask.Size(); size > PairMask {
		return errInvalidCIDRSize
	}

	t.prefix, err = t.ipam.NewPrefix(ctx, t.cidr)
	if err != nil {
		return err
	}

	return nil
}

func (t *IPTable) AvailablePairs() uint64 {
	return t.prefix.Usage().AvailableSmallestPrefixes
}

func (t *IPTable) AvailableIPs() uint64 {
	return t.prefix.Usage().AvailableIPs - GatewayReserved
}

func (t *IPTable) GetIP(ctx context.Context) (*IP, error) {
	ip, err := t.ipam.AcquireIP(ctx, t.prefix.Cidr)
	if err != nil {
		return nil, err
	}

	return NewIP(ip), nil
}

func (t *IPTable) ReleaseIP(ctx context.Context, ip *IP) error {
	if _, err := t.ipam.ReleaseIP(ctx, ip.IP); err != nil {
		return err
	}

	return nil
}

func (t *IPTable) GetPair(ctx context.Context) (*IPPair, error) {
	prefix, err := t.ipam.AcquireChildPrefix(ctx, t.prefix.Cidr, PairMask)
	if err != nil {
		return nil, err
	}

	firstIP, err := t.ipam.AcquireIP(ctx, prefix.Cidr)
	if err != nil {
		return nil, err
	}

	secondIP, err := t.ipam.AcquireIP(ctx, prefix.Cidr)
	if err != nil {
		return nil, err
	}

	return NewIPPair(
		NewIP(firstIP),
		NewIP(secondIP),

		prefix,
		t,
	), nil
}

func (i *IPTable) ReleasePair(ctx context.Context, ipPair *IPPair) error {
	if err := ipPair.ipTable.ReleaseIP(ctx, ipPair.GetFirstIP()); err != nil {
		return err
	}

	if err := ipPair.ipTable.ReleaseIP(ctx, ipPair.GetSecondIP()); err != nil {
		return err
	}

	return ipPair.ipTable.ipam.ReleaseChildPrefix(ctx, ipPair.prefix)
}

type IPPair struct {
	firstIP  *IP
	secondIP *IP

	prefix  *ipam.Prefix
	ipTable *IPTable
}

func NewIPPair(
	firstIP *IP,
	secondIP *IP,

	prefix *ipam.Prefix,
	ipTable *IPTable,
) *IPPair {
	return &IPPair{
		firstIP:  firstIP,
		secondIP: secondIP,

		prefix:  prefix,
		ipTable: ipTable,
	}
}

func (p *IPPair) GetFirstIP() *IP {
	return p.firstIP
}

func (p *IPPair) GetSecondIP() *IP {
	return p.secondIP
}

type IP struct {
	*ipam.IP
}

func NewIP(ip *ipam.IP) *IP {
	return &IP{ip}
}

func (i *IP) String() string {
	return i.IP.IP.String()
}
