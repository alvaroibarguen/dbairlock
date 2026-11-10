package connector

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// dbairlock runs inside the private network of its targets, so private
// addresses are normal. Link-local addresses are blocked because that is
// where cloud metadata services live (169.254.169.254, fe80::/10).

// CheckAddr rejects addresses a connection must never be made to.
func CheckAddr(a netip.Addr) error {
	a = a.Unmap()
	if a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return fmt.Errorf("connections to link-local address %s are not allowed", a)
	}
	if a.IsUnspecified() || a.IsMulticast() {
		return fmt.Errorf("address %s is not a valid target", a)
	}
	return nil
}

// CheckHost rejects a host that is a literal blocked address. Names are
// checked at dial time by SafeDialContext.
func CheckHost(host string) error {
	if a, err := netip.ParseAddr(host); err == nil {
		return CheckAddr(a)
	}
	return nil
}

// SafeDialContext dials like net.Dialer.DialContext, but resolves the host
// first and refuses blocked addresses (see CheckAddr). It dials the checked
// IP, so a DNS answer cannot change between the check and the dial.
func SafeDialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if err := CheckAddr(ip); err != nil {
				lastErr = err
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no addresses found for %s", host)
		}
		return nil, lastErr
	}
}
