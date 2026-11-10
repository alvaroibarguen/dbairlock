package identity

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// errBearer is the response to a missing or unusable token. It does not say
// which check failed.
var errBearer = httpx.NewError(http.StatusUnauthorized, "unauthorized",
	"A valid API token is required: send it as \"Authorization: Bearer dbal_pat_...\".")

// errBearerLimited answers a client address that sent too many bad tokens.
var errBearerLimited = httpx.NewError(http.StatusTooManyRequests, "rate_limited",
	"Too many requests with an invalid API token. Wait a moment, then check the token.")

// RequireBearer authenticates API-token requests (the /mcp endpoint). It
// rejects a missing, unknown, revoked or expired token and a token whose
// owner was removed, and otherwise puts the token owner's principal (via
// mcp, with the token id) and audit actor in the request context.
func (s *Service) RequireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dbairlock"`)
			httpx.WriteError(w, r, errBearer)
			return
		}
		ip := httpx.ClientIPFrom(r.Context())
		if d := s.tokenBackoff.Wait(ip); d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
			httpx.WriteError(w, r, errBearerLimited)
			return
		}
		p, err := s.AuthenticateToken(r.Context(), strings.TrimSpace(token))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dbairlock", error="invalid_token"`)
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
