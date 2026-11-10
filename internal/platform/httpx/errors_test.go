package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelopeBody {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var env struct {
		Error *envelopeBody `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if env.Error == nil {
		t.Fatalf("no error object in %q", rec.Body.String())
	}
	return *env.Error
}

func TestWriteErrorTyped(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	WriteError(rec, req, fmt.Errorf("wrapped: %w", NewError(http.StatusConflict, "conflict", "Already exists.")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body.Code != "conflict" || body.Message != "Already exists." {
		t.Fatalf("body = %+v", body)
	}
}

func TestWriteErrorHidesInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(logx.WithLogger(req.Context(), logx.Discard()))
	WriteError(rec, req, errors.New("dial tcp 10.0.0.1: password=hunter2"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body.Code != "internal" {
		t.Fatalf("code = %q", body.Code)
	}
	if got := rec.Body.String(); strings.Contains(got, "hunter2") || strings.Contains(got, "10.0.0.1") {
		t.Fatalf("internal details leaked: %s", got)
	}
}

func TestErrorString(t *testing.T) {
	if got := ErrNotFound.Error(); got != "not_found: The requested resource was not found." {
		t.Fatalf("Error() = %q", got)
	}
}
