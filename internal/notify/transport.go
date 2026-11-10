package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/secrets"
)

// errBlockedAddress means the webhook host resolves only to addresses that
// notifications may not be sent to.
var errBlockedAddress = errors.New("notify: address not allowed")

// checkAddr refuses addresses a webhook must never reach: loopback and
// link-local (where cloud metadata services live) unless allowLoopback, and
// never unspecified or multicast ones. Private addresses are allowed:
// dbairlock runs inside a private network and so may a chat relay.
func checkAddr(a netip.Addr, allowLoopback bool) error {
	a = a.Unmap()
	if a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return errBlockedAddress
	}
	if !allowLoopback && (a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast()) {
		return errBlockedAddress
	}
	return nil
}

// safeDial resolves the host, checks every address and dials a checked IP,
// so a DNS answer cannot change between the check and the dial.
func safeDial(allowLoopback bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: Timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		lastErr := errBlockedAddress
		for _, ip := range ips {
			if err := checkAddr(ip, allowLoopback); err != nil {
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// newClient returns the webhook client: a 5-second limit per attempt, no
// redirects followed, no proxy from the environment, and the address check.
func newClient(allowLoopback bool) *http.Client {
	return &http.Client{
		Timeout: Timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           safeDial(allowLoopback),
			TLSHandshakeTimeout:   Timeout,
			ResponseHeaderTimeout: Timeout,
			MaxIdleConns:          2,
			IdleConnTimeout:       Timeout,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// maxResponseRead bounds how much of a response body is read and discarded.
const maxResponseRead = 64 << 10

// post sends body to the secret URL. On failure it returns a fixed sentence:
// errors from net/http quote the URL, so they are classified, never passed
// on.
func post(ctx context.Context, client *http.Client, target secrets.Value, body []byte) (int, string) {
	raw := string(target.Reveal())
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return 0, "The webhook URL is not an http:// or https:// URL."
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, "The webhook URL is not valid."
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dbairlock-webhook")
	resp, err := client.Do(req)
	if err != nil {
		return 0, describe(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, ""
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return resp.StatusCode, fmt.Sprintf("The receiver answered %d (a redirect, which is not followed).", resp.StatusCode)
	default:
		return resp.StatusCode, fmt.Sprintf("The receiver answered %d.", resp.StatusCode)
	}
}

// describe turns a transport error into a sentence without the URL.
func describe(err error) string {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, errBlockedAddress):
		return "The webhook host is a loopback, link-local or otherwise blocked address."
	case errors.Is(err, context.Canceled):
		return "The delivery was cancelled."
	case errors.As(err, &dnsErr):
		return "The webhook host name could not be resolved."
	case errors.As(err, &netErr) && netErr.Timeout():
		return "The receiver did not answer within 5 seconds."
	default:
		return "The connection to the receiver failed."
	}
}
