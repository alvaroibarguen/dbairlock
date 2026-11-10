package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

func TestChainOrder(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "h") }), mk("a"), mk("b"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := strings.Join(order, ","); got != "a,b,h" {
		t.Fatalf("order = %s", got)
	}
}

func TestRequestIDAndAccessLog(t *testing.T) {
	var buf bytes.Buffer
	base := logx.New(&buf, false)
	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var seenID string
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFrom(r.Context())
		fc.Advance(150 * time.Millisecond)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("abc"))
	}), RequestID(&ids.Sequential{Prefix: "req"}, base), AccessLog(fc))

	req := httptest.NewRequest(http.MethodGet, "/some/path?token=secret", nil)
	req.Header.Set(RequestIDHeader, "client-supplied\nforged")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "req-000001" {
		t.Fatalf("response request id = %q", got)
	}
	if seenID != "req-000001" {
		t.Fatalf("context request id = %q", seenID)
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("access log not one JSON line: %q", buf.String())
	}
	want := map[string]any{
		"msg": "http request", "method": "GET", "path": "/some/path",
		"status": float64(418), "bytes": float64(3), "request_id": "req-000001",
		"duration": float64(150 * time.Millisecond),
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("log[%q] = %v, want %v", k, line[k], v)
		}
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("query string was logged: %s", buf.String())
	}
}

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	ctxLogger := RequestID(&ids.Sequential{Prefix: "r"}, logx.New(&buf, false))
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), ctxLogger, Recover())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := decodeEnvelope(t, rec); body.Code != "internal" {
		t.Fatalf("code = %q", body.Code)
	}
	if !strings.Contains(buf.String(), "panic in handler") || !strings.Contains(buf.String(), `"request_id":"r-000001"`) {
		t.Fatalf("panic not logged with request id: %s", buf.String())
	}
}

func TestRecoverReraisesAbortHandler(t *testing.T) {
	h := Recover()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // sentinel identity is the point
			t.Fatalf("recovered %v, want ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestBodyLimit(t *testing.T) {
	var readErr error
	h := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		if readErr != nil {
			var mbe *http.MaxBytesError
			if errors.As(readErr, &mbe) {
				WriteError(w, r, ErrRequestTooLarge)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Run("within limit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345678")))
		if rec.Code != http.StatusNoContent || readErr != nil {
			t.Fatalf("status = %d, err = %v", rec.Code, readErr)
		}
	})
	t.Run("declared too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("123456789")))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("streamed too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(strings.NewReader("123456789")))
		req.ContentLength = -1
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", rec.Code)
		}
		if body := decodeEnvelope(t, rec); body.Code != "request_too_large" {
			t.Fatalf("code = %q", body.Code)
		}
	})
}
