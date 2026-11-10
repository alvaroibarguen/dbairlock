package connector

import (
	"encoding/base64"
	"unicode/utf8"
)

// Value type tags used by TaggedValue.
const (
	TagBytes = "bytes" // binary data; Value is base64
	TagText  = "text"  // a text value that had to be truncated
)

// TaggedValue is a cell value that is not plain JSON: binary data, or a
// value truncated for display. Display truncation happens after the row
// fingerprint was computed over the complete value.
type TaggedValue struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Size      int    `json:"size"`                // size of the complete value in bytes
	Truncated bool   `json:"truncated,omitempty"` // Value holds only a prefix
}

// TruncateText cuts s to at most max bytes at a UTF-8 boundary. It returns s
// unchanged and false when it fits, or when max <= 0 (no limit).
func TruncateText(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// TextValue returns s as a display value: the string itself, or a truncated
// TaggedValue when it is longer than maxBytes. The bool reports truncation.
func TextValue(s string, maxBytes int) (any, bool) {
	cut, truncated := TruncateText(s, maxBytes)
	if !truncated {
		return s, false
	}
	return TaggedValue{Type: TagText, Value: cut, Size: len(s), Truncated: true}, true
}

// BytesValue returns b as a base64 TaggedValue, keeping at most maxBytes of
// it. The bool reports truncation.
func BytesValue(b []byte, maxBytes int) (TaggedValue, bool) {
	size := len(b)
	truncated := false
	if maxBytes > 0 && len(b) > maxBytes {
		b = b[:maxBytes]
		truncated = true
	}
	return TaggedValue{Type: TagBytes, Value: base64.StdEncoding.EncodeToString(b), Size: size, Truncated: truncated}, truncated
}
