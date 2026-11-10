package secrets

import (
	"fmt"
	"log/slog"
)

// Redacted is what every representation of a Value prints.
const Redacted = "[redacted]"

// Value is a secret in memory. Its string, Go-syntax, fmt (any verb), JSON,
// text and slog representations are all "[redacted]"; the only way to the
// bytes is Reveal, which a lint rule (forbidigo) allows only in targets,
// identity and notify.
//
// Go cannot guarantee that memory is zeroed, so Value makes no such claim.
// Keep revealed bytes scoped to the function that needs them and never cache
// plaintext.
type Value struct {
	b []byte
}

// NewValue copies s into a Value.
func NewValue(s string) Value { return Value{b: []byte(s)} }

// NewValueBytes copies b into a Value.
func NewValueBytes(b []byte) Value { return Value{b: append([]byte(nil), b...)} }

// IsEmpty reports whether the value has no bytes.
func (v Value) IsEmpty() bool { return len(v.b) == 0 }

// Len is the value's length in bytes, for input validation only. It is never
// stored or returned: Meta carries nothing derived from the value.
func (v Value) Len() int { return len(v.b) }

// Reveal returns a copy of the secret bytes. Allowed only in targets,
// identity and notify (enforced by forbidigo), at the moment of use.
func (v Value) Reveal() []byte { return append([]byte(nil), v.b...) }

// String implements fmt.Stringer.
func (Value) String() string { return Redacted }

// GoString implements fmt.GoStringer (%#v).
func (Value) GoString() string { return Redacted }

// Format implements fmt.Formatter so that every verb, including %d and %x
// which would otherwise print the underlying bytes, is redacted.
func (Value) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(Redacted)) }

// MarshalJSON implements json.Marshaler.
func (Value) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler.
func (Value) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// LogValue implements slog.LogValuer.
func (Value) LogValue() slog.Value { return slog.StringValue(Redacted) }

// SecretMarker marks Value for audit.Recorder, which rejects any payload that
// contains one (see audit.SecretMarker).
func (Value) SecretMarker() {}
