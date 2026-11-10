package httpx

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Cache-Control values used by the SPA handler.
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNone      = "no-cache"
)

// notBuiltPage is served when the embedded file system has no index.html,
// which happens when the binary was built with `go build` instead of
// `make build`.
const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>dbairlock</title></head>
<body><p>The dashboard has not been built into this binary. Run <code>make build</code>,
or use <code>make dev</code> and open the Vite dev server.</p></body></html>
`

// SPA serves a built single-page app from fsys.
//
//   - Paths under /api/ and /mcp never fall back to the app; unknown ones get
//     a JSON 404 so API clients never receive HTML.
//   - Existing files are served. Files under /assets/ have content-hashed
//     names and are cached for a year; everything else must be revalidated.
//   - Unknown paths without a file extension get index.html, so client-side
//     routes work on reload. Unknown paths with an extension, and anything
//     under /assets/, get a 404.
//   - Dotfiles are never served.
func SPA(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if isAPIPath(p) {
			WriteError(w, r, ErrNotFound)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			WriteError(w, r, ErrMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+p), "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, fsys)
			return
		}
		if hasDotSegment(name) {
			http.NotFound(w, r)
			return
		}
		if serveFile(w, r, fsys, name) {
			return
		}
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, fsys)
	})
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/mcp" || strings.HasPrefix(p, "/mcp/")
}

func hasDotSegment(name string) bool {
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// serveFile serves a regular file and reports whether it existed.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheNone)
	}
	http.ServeContent(w, r, name, info.ModTime(), rs)
	return true
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	f, err := fsys.Open("index.html")
	if errors.Is(err, fs.ErrNotExist) {
		w.Header().Set("Cache-Control", cacheNone)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(notBuiltPage))
		return
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		WriteError(w, r, errors.New("spa: index.html is not seekable"))
		return
	}
	w.Header().Set("Cache-Control", cacheNone)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", info.ModTime(), rs)
}
