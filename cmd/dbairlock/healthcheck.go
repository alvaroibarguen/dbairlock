package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// healthcheckTimeout bounds the whole probe, so a hung server reads as
// unhealthy rather than blocking the container runtime's check.
const healthcheckTimeout = 3 * time.Second

// healthcheck probes the local server's /readyz and exits 0 when it answers
// 200, 1 otherwise. /readyz pings the metadata store, so a server whose store
// is closed or unreachable reports unhealthy, not only a dead process. It
// exists for the image's HEALTHCHECK: a distroless image has no shell or curl.
func healthcheck(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8080", "the server's listen address; the probe connects to its port on loopback")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	url, err := healthcheckURL(*listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %s returned %s\n", url, resp.Status)
		return 1
	}
	return 0
}

// healthcheckURL turns a listen address (":8080", "0.0.0.0:8080",
// "[::]:8080", "127.0.0.1:8080") into the loopback /readyz URL. A specific
// non-wildcard host is kept, since the server may listen only there.
func healthcheckURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid --listen %q: %w", listen, err)
	}
	if port == "" {
		return "", fmt.Errorf("invalid --listen %q: no port", listen)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}
