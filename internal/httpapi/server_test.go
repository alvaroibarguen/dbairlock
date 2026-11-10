package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

// fakeSetup implements SetupRequired; the other Setup methods are not
// called by these tests (the embedded nil interface would panic).
type fakeSetup struct {
	Setup
	required bool
	err      error
}

func (f fakeSetup) SetupRequired(context.Context) (bool, error) { return f.required, f.err }

type fakeReady struct{ err error }

func (f fakeReady) Ready(context.Context) error { return f.err }

func newMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	if err := New(deps).Register(mux); err != nil {
		panic(err)
	}
	return mux
}

func do(t *testing.T, h http.Handler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(logx.WithLogger(req.Context(), logx.Discard()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestHealthz(t *testing.T) {
	rec, body := do(t, newMux(Deps{Setup: fakeSetup{}, Readiness: fakeReady{}}), "GET", "/healthz")
	if rec.Code != 200 || body["status"] != "ok" {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

func TestReadyz(t *testing.T) {
	rec, _ := do(t, newMux(Deps{Setup: fakeSetup{}, Readiness: fakeReady{}}), "GET", "/readyz")
	if rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	rec, body := do(t, newMux(Deps{Setup: fakeSetup{}, Readiness: fakeReady{err: errors.New("db closed")}}), "GET", "/readyz")
	if rec.Code != 503 {
		t.Fatalf("not ready: %d", rec.Code)
	}
	if e, _ := body["error"].(map[string]any); e["code"] != "not_ready" {
		t.Fatalf("body = %v", body)
	}
}

func TestMeta(t *testing.T) {
	for _, tc := range []struct {
		required bool
		want     string
	}{{false, "ready"}, {true, "setup_required"}} {
		rec, body := do(t, newMux(Deps{Version: "v1.2.3", Setup: fakeSetup{required: tc.required}, Readiness: fakeReady{}}), "GET", "/api/meta")
		if rec.Code != 200 || body["version"] != "v1.2.3" || body["setupState"] != tc.want {
			t.Fatalf("%d %v", rec.Code, body)
		}
	}
}

func TestMetaInternalErrorIsGeneric(t *testing.T) {
	rec, body := do(t, newMux(Deps{Setup: fakeSetup{err: errors.New("sqlite: disk I/O error")}, Readiness: fakeReady{}}), "GET", "/api/meta")
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	if e, _ := body["error"].(map[string]any); e["code"] != "internal" {
		t.Fatalf("body = %v", body)
	}
	if strings.Contains(rec.Body.String(), "sqlite") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
}

func TestMapError(t *testing.T) {
	sentinel := errors.New("thing not found")
	errorTable = append(errorTable, struct {
		err  error
		http *httpx.Error
	}{sentinel, httpx.ErrNotFound})
	t.Cleanup(func() { errorTable = errorTable[:len(errorTable)-1] })

	var he *httpx.Error
	if !errors.As(mapError(fmt.Errorf("load: %w", sentinel)), &he) || he.Status != http.StatusNotFound {
		t.Fatalf("sentinel not mapped: %v", he)
	}
	other := errors.New("boom")
	if !errors.Is(mapError(other), other) {
		t.Fatal("unknown error should pass through")
	}
}

func TestRequestErrorTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	requestError(rec, httptest.NewRequest("POST", "/api/x", nil), fmt.Errorf("decode: %w", &http.MaxBytesError{Limit: 1}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}
