package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPKey struct{}

// ClientIP stores the client's IP address in the request context. The direct
// peer's address is used unless that peer is a trusted proxy (trusted returns
// the current list, so it can follow a setting); then X-Forwarded-For is read
// from the right, skipping trusted proxies, and the first untrusted address
// is the client. A client cannot forge its address through a proxy that is
// not trusted.
func ClientIP(trusted func() []netip.Prefix) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, trusted())
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

// ClientIPFrom returns the address stored by ClientIP, or "".
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return ""
	}
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	hops := r.Header.Values("X-Forwarded-For")
	var all []string
	for _, h := range hops {
		all = append(all, strings.Split(h, ",")...)
	}
	for i := len(all) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(all[i]))
		if err != nil {
			break // a malformed hop: stop at the last address we can trust
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) {
			return a.String()
		}
		peer = a
	}
	return peer.String()
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
