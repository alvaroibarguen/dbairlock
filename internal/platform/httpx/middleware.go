package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

// DefaultMaxBodyBytes is the request body limit applied to every request.
// A compile-time constant: no endpoint needs more, and a larger limit only
// widens the denial-of-service surface.
const DefaultMaxBodyBytes int64 = 1 << 20

// RequestIDHeader carries the request id on every response.
const RequestIDHeader = "X-Request-Id"

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first one listed is the outermost.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type requestIDKey struct{}

// RequestID gives every request a fresh id, returns it in the X-Request-Id
// header, and stores a logger tagged with it in the request context. Ids sent
// by clients are ignored so they cannot be used to forge log lines.
func RequestID(gen ids.Generator, base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := gen.NewID()
			w.Header().Set(RequestIDHeader, id)
			ctx := r.Context()
			ctx = contextWithRequestID(ctx, id)
			ctx = logx.WithLogger(ctx, base.With(slog.String("request_id", id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog logs one line per request with method, path, status, size and
// duration. The query string is not logged.
func AccessLog(c clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := c.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			logx.FromContext(r.Context()).LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("bytes", rec.bytes),
				slog.Duration("duration", c.Now().Sub(start)),
			)
		})
	}
}

// Recover turns a panic in a handler into a logged 500 with the standard
// error envelope. http.ErrAbortHandler is re-raised, as net/http expects.
func Recover() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				logx.FromContext(r.Context()).ErrorContext(r.Context(), "panic in handler",
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())),
				)
				if rec.status == 0 {
					WriteError(rec, r, ErrInternal)
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// BodyLimit caps the request body at n bytes. Reading past the limit returns
// an *http.MaxBytesError, which handlers map to ErrRequestTooLarge. Requests
// that declare a larger Content-Length are rejected up front.
func BodyLimit(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				WriteError(w, r, ErrRequestTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status code and body size written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush supports streaming responses through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
