package identity

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Pure checks of the OAuth protocol values: redirect URIs, PKCE and resource
// indicators. They make no store access, so they are tested in isolation.

const maxRedirectURILen = 512

// loopbackHosts are the only redirect hosts accepted (RFC 8252 section 7.3:
// native apps receive the code on a loopback port they open). Written as
// url.URL.Host renders them without a port.
var loopbackHosts = []string{"127.0.0.1", "localhost", "[::1]"}

// redirectPath allows the plain path characters a native client uses
// ("/callback", "/oauth/callback"); anything encoded or unusual is refused.
var redirectPath = regexp.MustCompile(`^(/[A-Za-z0-9._~-]*)*$`)

// loopbackRedirect is a parsed redirect URI: http, a loopback host, an
// optional port and a plain path.
type loopbackRedirect struct {
	host string // as in loopbackHosts
	port string // "" when absent
	path string
}

// parseLoopbackRedirect accepts only http://127.0.0.1, http://localhost or
// http://[::1], with any port, and a plain path: no user info, query,
// fragment, percent-encoding, dot segments or anything that would read
// differently after normalization. It returns ok=false for everything else.
func parseLoopbackRedirect(raw string) (loopbackRedirect, bool) {
	if raw == "" || len(raw) > maxRedirectURILen || !strings.HasPrefix(raw, "http://") ||
		strings.ContainsAny(raw, "@?#%\\ \t\r\n") {
		return loopbackRedirect{}, false
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return loopbackRedirect{}, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.String() != raw {
		return loopbackRedirect{}, false
	}
	host, port := u.Host, ""
	if i := strings.LastIndex(u.Host, ":"); i >= 0 && !strings.HasSuffix(u.Host, "]") {
		host, port = u.Host[:i], u.Host[i+1:]
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return loopbackRedirect{}, false
		}
	}
	ok := false
	for _, h := range loopbackHosts {
		ok = ok || host == h
	}
	if !ok || !redirectPath.MatchString(u.Path) {
		return loopbackRedirect{}, false
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg == "." || seg == ".." {
			return loopbackRedirect{}, false
		}
	}
	return loopbackRedirect{host: host, port: port, path: u.Path}, true
}

// redirectMatches reports whether the redirect URI of an authorization
// request matches one registered for the client: the same loopback host and
// the exact same path, on any port (RFC 8252 section 7.3).
func redirectMatches(registered []string, requested string) bool {
	req, ok := parseLoopbackRedirect(requested)
	if !ok {
		return false
	}
	for _, r := range registered {
		reg, ok := parseLoopbackRedirect(r)
		if ok && reg.host == req.host && reg.path == req.path {
			return true
		}
	}
	return false
}

var (
	pkceVerifier  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// validChallenge reports whether c can be an S256 code challenge: the
// unpadded base64url encoding of a SHA-256 digest.
func validChallenge(c string) bool { return pkceChallenge.MatchString(c) }

// pkceS256 is the S256 code challenge of verifier (RFC 7636 section 4.2).
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// verifyPKCE checks a code verifier against the stored S256 challenge.
func verifyPKCE(verifier, challenge string) bool {
	if !pkceVerifier.MatchString(verifier) || !validChallenge(challenge) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pkceS256(verifier)), []byte(challenge)) == 1
}

// canonicalURL lower-cases the scheme and host of an absolute http(s) URL
// and removes a trailing slash. ok is false for anything else, including a
// URL with user info, a query or a fragment (RFC 8707 section 2).
func canonicalURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(raw, "#") || u.Opaque != "" {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/"), true
}

// resourceMatches reports whether a requested resource indicator names
// this server's MCP endpoint. The MCP URL itself is canonical; the bare
// instance URL is accepted as the same resource, because clients that read
// the root protected resource metadata (RFC 9728 section 3) send it, and
// dbairlock protects nothing else.
func resourceMatches(requested, issuer, resource string) bool {
	c, ok := canonicalURL(requested)
	return ok && (c == resource || c == issuer)
}
