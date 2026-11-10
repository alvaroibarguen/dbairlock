package identity

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// errBearer is the response to a missing or unusable token. It does not say
// which check failed.
var errBearer = httpx.NewError(http.StatusUnauthorized, "unauthorized",
	"A valid API token is required: send it as \"Authorization: Bearer dbal_pat_...\", or sign in with OAuth.")

// errBearerLimited answers a client address that sent too many bad tokens.
var errBearerLimited = httpx.NewError(http.StatusTooManyRequests, "rate_limited",
	"Too many requests with an invalid API token. Wait a moment, then check the token.")

// challenge is the WWW-Authenticate header of a 401 from /mcp. While OAuth
// is available it names the protected resource metadata and the scope, so
// MCP clients can discover the authorization server (RFC 9728 section 5.1).
func (s *Service) challenge(invalid bool) string {
	h := `Bearer realm="dbairlock"`
	if srv, err := s.OAuthServer(); err == nil {
		h += `, resource_metadata="` + srv.ResourceMetadataURL() + `", scope="` + OAuthScope + `"`
	}
	if invalid {
		h += `, error="invalid_token"`
	}
	return h
}

// authenticateBearer authenticates an API token or an OAuth access token.
func (s *Service) authenticateBearer(ctx context.Context, token string) (Principal, error) {
	if strings.HasPrefix(token, OAuthAccessPrefix) {
		return s.AuthenticateOAuthToken(ctx, token)
	}
	return s.AuthenticateToken(ctx, token)
}

// RequireBearer authenticates agent requests (the /mcp endpoint) by API
// token or OAuth access token. It rejects a missing, unknown, revoked or
// expired token and a token whose owner was removed, and otherwise puts the
// owner's principal (via mcp, with the token or grant id) and audit actor in
// the request context.
func (s *Service) RequireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			w.Header().Set("WWW-Authenticate", s.challenge(false))
			httpx.WriteError(w, r, errBearer)
			return
		}
		ip := httpx.ClientIPFrom(r.Context())
		if d := s.tokenBackoff.Wait(ip); d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
			httpx.WriteError(w, r, errBearerLimited)
			return
		}
		p, err := s.authenticateBearer(r.Context(), strings.TrimSpace(token))
		if err != nil {
			w.Header().Set("WWW-Authenticate", s.challenge(true))
			if err == ErrUnauthenticated {
				s.tokenBackoff.Fail(ip)
				httpx.WriteError(w, r, errBearer)
			} else {
				httpx.WriteError(w, r, err)
			}
			return
		}
		s.tokenBackoff.Reset(ip)
		ctx := WithPrincipal(r.Context(), p, ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
