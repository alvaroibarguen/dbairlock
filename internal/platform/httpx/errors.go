// Package httpx holds the HTTP plumbing shared by every transport: the error
// type and its JSON envelope, middleware, and the single-page-app handler.
// It has no domain knowledge.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

// Error is the only error type rendered to HTTP clients. Code is a stable
// machine-readable identifier; Message is safe to show to a person.
type Error struct {
	Status  int
	Code    string
	Message string
	// Fields lists invalid fields on a validation error.
	Fields []FieldError
}

// FieldError names one invalid field and why, safe to show to a person.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError returns an *Error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// NewValidationError returns a 422 validation_failed error listing fields.
func NewValidationError(message string, fields []FieldError) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: message, Fields: fields}
}

// Common errors.
var (
	ErrUnauthorized     = NewError(http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
	ErrForbidden        = NewError(http.StatusForbidden, "forbidden", "You do not have permission to do this.")
	ErrNotFound         = NewError(http.StatusNotFound, "not_found", "The requested resource was not found.")
	ErrMethodNotAllowed = NewError(http.StatusMethodNotAllowed, "method_not_allowed", "This method is not allowed on this resource.")
	ErrRequestTooLarge  = NewError(http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.")
	ErrInternal         = NewError(http.StatusInternalServerError, "internal", "An internal error occurred.")
)

// envelope is the wire shape: {"error":{"code":"...","message":"..."}}.
type envelope struct {
	Error envelopeBody `json:"error"`
}

type envelopeBody struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

// WriteError renders err as the JSON error envelope. An *Error (possibly
// wrapped) is rendered as is. Any other error is logged with the request's
// logger and rendered as a generic 500, so internal details never reach the
// client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var he *Error
	if !errors.As(err, &he) {
		logx.FromContext(r.Context()).ErrorContext(r.Context(), "internal error",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		he = ErrInternal
	}
	WriteJSON(w, he.Status, envelope{Error: envelopeBody{Code: he.Code, Message: he.Message, Fields: he.Fields}})
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"internal","message":"An internal error occurred."}}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
