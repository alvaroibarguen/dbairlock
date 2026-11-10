# Phase 11 notes: web security and dependencies (workstream `websec`)

Details of the final state are in docs/security.md ("Web security") and
docs/VERSIONS.md ("Vulnerability scans"). This file records what was tried
and found.

## Content Security Policy

- **Before:** `style-src 'self' 'unsafe-inline'`, `img-src 'self' data:`,
  `base-uri 'self'`, and the CSP had never been checked in a browser.
- **Tried first:** `style-src 'self'` with nothing else. Running the
  Playwright suite with a new violation check (every test fails on any CSP
  violation, `web/e2e/csp.ts`) found four sources of inline style or eval:
  1. sonner (toasts) inserts a fixed `<style>` at import, appending it empty
     and then adding the text: two violations per page load.
  2. Base UI's Select popup renders a `<style>` (React-hoisted) to hide its
     scrollbar.
  3. CodeMirror mounts its styles in a `<style>` element. It uses
     constructed stylesheets only inside shadow roots, not for the document,
     contrary to what I first assumed.
  4. zod 4 probes `new Function` on first object parse; the browser reports
     the blocked eval even though zod catches it.
- **Fixed without `'unsafe-inline'`:** sonner by two hashes (with a unit test
  that recomputes them from the installed package), Base UI by
  `CSPProvider disableStyleElements` plus the same CSS rule in index.css,
  CodeMirror by a per-response style nonce (the SPA handler injects
  `<meta name="csp-nonce">` and serves the page `no-store` without
  validators; the editor passes it to `EditorView.cspNonce`), zod by
  `z.config({ jitless: true })`.
- **Final policy:** `default-src 'self'; script-src 'self'; style-src 'self'
  <2 hashes> [nonce on the page]; img-src 'self'; font-src 'self';
  connect-src 'self'; object-src 'none'; base-uri 'none'; form-action
  'self'; frame-ancestors 'none'`. The full Playwright suite (44 tests,
  desktop and mobile, both sample databases, the SQL editor, toasts,
  dialogs, selects) passes with zero violations.
- **Risk to know about:** a sonner upgrade that changes its CSS breaks toast
  styling until the hash is updated; `web/src/test/csp.test.ts` fails first
  and prints the new hash. A Base UI upgrade that adds another inline
  `<style>` or prehydration `<script>` would be caught by the e2e check.

## Headers

- Referrer-Policy changed from `same-origin` to `no-referrer`;
  Permissions-Policy extended; HSTS (`max-age=31536000`, no
  includeSubDomains/preload) added, sent only while `instance.public_url` is
  https, read per request.
- The header middleware moved to the outermost position (it was inside
  RequestID/AccessLog/Recover); tests now cover SPA, assets, API, MCP,
  health, 404, 405, 413, cross-origin 403 and the 401s, with and without an
  https public URL.
- Not covered: responses Go's `http.Server` writes itself before the handler
  (e.g. malformed request line), which are short plain-text bodies.

## Route access

- Before: authorization lived only inside each handler (`requireUser` /
  `authorize`); nothing declared a route's access level.
- Added `internal/httpapi/access.go`: an access level for every operationId,
  enforced by the strict middleware before the handler (fail closed: an
  operation without a declaration gets 403 and an error log). Non-OpenAPI
  routes (`/mcp`, `/`) declare theirs where app.go registers them; the root
  mux records every registration.
- `TestRouteAccess` walks the recorded route list and checks declaration,
  cross-origin rejection of every mutating method (with a real admin
  session), 401/403/404 by level, and that a session cookie does not
  authenticate `/mcp`. No route was found unprotected. One behaviour noted:
  `GET /api/setup/google/start` after setup answers 302 to
  `/setup?error=...` rather than a 4xx; it is a browser navigation and never
  reaches Google, so the test accepts exactly that.

## Rate limits

- Sign-in, setup code, recovery code and MCP per-token tool limits existed.
  Tests existed for the setup code and MCP tool limits only. Added tests for
  sign-in (start and callback share the limit) and recovery codes.
- Gap found and fixed: requests to `/mcp` with a bad API token were not
  limited at all (each one a database lookup). Added a per-address backoff
  (10 free failures, then 1 s doubling to 5 min, reset by a valid token) and
  `TestBadAPITokensAreRateLimited`. Trade-off: during a lock-out a valid
  token from the same address also waits (agents behind one NAT).

## HTML injection

- None found: no `dangerouslySetInnerHTML`, `innerHTML` or similar in
  `web/src`; all `href`s are fixed paths. Added an ESLint
  `no-restricted-syntax` rule (plus `no-eval`, `no-new-func`,
  `no-script-url`) and a test that each banned pattern is still reported.

## Dependencies

- `go tool govulncheck ./...`: no vulnerabilities.
- `pnpm audit`: one moderate, minimatch 10.2.6 (GHSA-g3hv-75wg-m249) under
  eslint only; no 10.x fix exists and eslint requires `^10`. Accepted and
  documented in VERSIONS.md (dev-only, repository-authored globs).
- `go mod tidy` changes nothing; every direct Go and npm dependency is
  imported. Nothing unexpected or unmaintained found.
- VERSIONS.md matched go.mod, the lockfile and the Dockerfile. Re-tried the
  packages pnpm's minimum release age held back: vite 8.3.6 -> 8.3.7 and
  `@vitejs/plugin-react` 6.2.1 -> 6.3.0 installed; typescript-eslint 8.72.2
  is still too young and stays at 8.72.1.

## Small things changed along the way

- `web/tsconfig.node.json` includes the DOM lib so e2e code passed to
  `page.evaluate` type-checks.
- The two ESLint-driven unit tests (`boundaries.test.ts`, the new
  `html-injection.test.ts`) got a 60 s timeout: the first type-aware lint
  timed out at 5 s when both ran in parallel on a loaded machine.

## Not verified

- Browsers other than Chromium (Playwright runs Desktop Chrome only).
  Firefox and Safari enforce the same CSP features used here, but that was
  not run.
- HSTS in a real HTTPS deployment (only unit and handler tests).
