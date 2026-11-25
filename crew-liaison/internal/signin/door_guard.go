package signin

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

var errBlockedAddress = errors.New("the address is not a public one")

// notPublic lists the ranges that are not the public internet and that netip does not already
// call private, loopback or link-local: shared address space, documentation and benchmarking
// ranges, reserved space, and the IPv6 forms that embed or translate an IPv4 address.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("::/96"), netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("192.88.99.0/24"),
}

// publicAddr reports whether a is an address on the public internet. An IPv4 address written as
// IPv6 is judged as the IPv4 address it is.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// guardedDialer resolves a name itself, requires every answer to be allowed, and then dials one
// of the checked addresses directly. The check and the connection use the same addresses, so a
// name that answers differently the second time (DNS rebinding) cannot reach an address that
// was not checked.
type guardedDialer struct {
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	allow  func(netip.Addr) bool
	dialer net.Dialer
}

func systemLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (g *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if !g.allow(a) {
			return nil, errBlockedAddress
		}
	}
	return g.dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].String(), port))
}

func (g *guardedDialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	addrs, err := g.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("the name did not resolve")
	}
	return addrs, nil
}
