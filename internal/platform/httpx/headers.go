package httpx

import "net/http"

// sonnerStyleHashes allow the one <style> element the dashboard creates at
// run time: the toast library (sonner) inserts its fixed stylesheet when it
// loads. It appends the element empty and then adds the text, so the browser
// checks both the empty string and the full text. Allowing exactly those two
// by hash keeps 'unsafe-inline' out of style-src. web/src/test/csp.test.ts
// recomputes the hash from the installed sonner and fails when an upgrade
// changes it; the Playwright suite fails on any CSP violation.
//
// The only other inline styles are CodeMirror's <style> element (allowed by
// a nonce, below) and style properties that React, Base UI and CodeMirror set
// through the CSSOM, which CSP does not govern. Base UI's own <style>
// elements are turned off in the dashboard (web/src/ui/StrictStyles.tsx).
const sonnerStyleHashes = "'sha256-StEaX+se6YS7pqjzrzMIA0KaX9zF/8zAhvQXZAe5epY=' " +
	"'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='" // the empty string

// ContentSecurityPolicy is sent on every response. Only same-origin scripts,
// styles, fonts, images and requests are allowed; no inline script, no eval,
// no plugins, no <base>, no framing.
var ContentSecurityPolicy = contentSecurityPolicy("")

// ContentSecurityPolicyWithStyleNonce is the policy for the dashboard page
// itself: the same, plus one per-response nonce for <style> elements. The
// SQL editor (CodeMirror) keeps its styles in a <style> element whose text
// changes as editors mount, so a hash cannot cover it; the SPA handler puts
// the nonce in a <meta name="csp-nonce"> tag and the editor sets it on that
// element (EditorView.cspNonce). The nonce never applies to scripts.
func ContentSecurityPolicyWithStyleNonce(nonce string) string {
	return contentSecurityPolicy(nonce)
}

func contentSecurityPolicy(styleNonce string) string {
	styles := "'self' " + sonnerStyleHashes
	if styleNonce != "" {
		styles += " 'nonce-" + styleNonce + "'"
	}
	return "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src " + styles + "; " +
		"img-src 'self'; " +
		"font-src 'self'; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
}

// PermissionsPolicy turns off browser features the dashboard never uses.
const PermissionsPolicy = "accelerometer=(), camera=(), display-capture=(), geolocation=(), " +
	"gyroscope=(), hid=(), magnetometer=(), microphone=(), midi=(), payment=(), " +
	"serial=(), usb=(), browsing-topics=()"

// HSTS is sent only when HTTPS reports true (the configured public URL is
// https). It leaves out includeSubDomains and preload: dbairlock usually runs
// on one host of a larger internal domain it does not control.
const HSTS = "max-age=31536000"

// SecurityHeaders sets defensive headers on every response: the dashboard,
// the API, MCP, health probes and errors alike. It must wrap every other
// handler so that even a panic or a rejected request carries them. https
// reports whether the instance is served over HTTPS; it is consulted on each
// request, so changing the public URL takes effect without a restart.
func SecurityHeaders(https func() bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", ContentSecurityPolicy)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Permissions-Policy", PermissionsPolicy)
			if https != nil && https() {
				h.Set("Strict-Transport-Security", HSTS)
			}
			next.ServeHTTP(w, r)
		})
	}
}
