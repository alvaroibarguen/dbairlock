package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// MaxPayloadString is the longest string value kept in an audit payload, in
// bytes. Longer strings are cut and marked by truncate. Not a setting: it
// bounds the size of one event (statements are at most 64 KB, and the
// full SQL stays in the change itself), and changing it would not alter
// past events, which keep their hashes.
const MaxPayloadString = 8 << 10

// hashSize is the length of prev_hash and hash. The first event's prev_hash
// is hashSize zero bytes.
const hashSize = sha256.Size

// record is exactly the set of columns that are hashed. It mirrors one row of
// audit_events minus prev_hash and hash.
type record struct {
	Seq         int64
	ID          string
	OccurredAt  int64 // milliseconds since the epoch, UTC; never a formatted time
	ActorUserID string
	ActorEmail  string
	Via         string
	APITokenID  string
	IP          string
	Action      string
	SubjectType string
	SubjectID   string
	Payload     string // canonical JSON object
}

// canonicalJSON is the one serialization the hash chain is computed over.
//
// DO NOT CHANGE IT. Every stored hash depends on these exact bytes; changing
// the field order, the escaping or the number format breaks verification of
// every existing row. The golden tests in canonical_test.go pin the output.
//
// Rules: fields in the fixed order below, no insignificant whitespace,
// strings escaped by encoding/json with HTML escaping off, integers in
// base 10, payload embedded as canonical JSON (see canonicalPayload).
func canonicalJSON(r record) ([]byte, error) {
	payload, err := canonicalPayload([]byte(r.Payload))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"seq":`)
	b.WriteString(strconv.FormatInt(r.Seq, 10))
	str := func(name, v string) {
		b.WriteString(`,"`)
		b.WriteString(name)
		b.WriteString(`":`)
		writeString(&b, v)
	}
	str("id", r.ID)
	b.WriteString(`,"occurred_at":`)
	b.WriteString(strconv.FormatInt(r.OccurredAt, 10))
	str("actor_user_id", r.ActorUserID)
	str("actor_email", r.ActorEmail)
	str("via", r.Via)
	str("api_token_id", r.APITokenID)
	str("ip", r.IP)
	str("action", r.Action)
	str("subject_type", r.SubjectType)
	str("subject_id", r.SubjectID)
	b.WriteString(`,"payload":`)
	b.Write(payload)
	b.WriteByte('}')
	return b.Bytes(), nil
}

// chainHash returns SHA-256(prev || canonicalJSON(r)).
func chainHash(prev []byte, r record) ([]byte, error) {
	c, err := canonicalJSON(r)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(c)
	return h.Sum(nil), nil
}

func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)       // encoding a string cannot fail
	b.Truncate(b.Len() - 1) // drop Encode's trailing newline
}

// canonicalPayload re-encodes a JSON object with object keys sorted at every
// level, no insignificant whitespace, HTML escaping off and numbers kept as
// their literal text. It is idempotent, so the stored payload (already
// canonical) hashes the same when the verifier re-canonicalizes it.
func canonicalPayload(raw []byte) ([]byte, error) {
	v, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	return encodeCanonical(v)
}

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("audit: payload is not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("audit: payload has trailing data")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("audit: payload must be a JSON object")
	}
	return obj, nil
}

func encodeCanonical(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// encoding/json writes map keys in sorted order and json.Number verbatim.
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("audit: encode payload: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// truncate walks a decoded payload and cuts every string value longer than
// MaxPayloadString bytes, appending a marker that records the original size.
// Object keys are left alone.
func truncate(v any) any {
	switch t := v.(type) {
	case string:
		if len(t) <= MaxPayloadString {
			return t
		}
		cut := MaxPayloadString
		for cut > 0 && !utf8.RuneStart(t[cut]) {
			cut--
		}
		return t[:cut] + fmt.Sprintf("...[truncated: %d bytes]", len(t))
	case map[string]any:
		for k, e := range t {
			t[k] = truncate(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = truncate(e)
		}
		return t
	default:
		return v
	}
}
