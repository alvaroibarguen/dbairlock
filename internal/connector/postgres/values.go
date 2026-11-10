package postgres

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Every query the connector runs returns text-format values, under session
// settings pinned per transaction (DateStyle ISO, TimeZone UTC, IntervalStyle
// postgres, extra_float_digits 1, bytea_output hex, lc_monetary C). The text
// is therefore canonical: numerics in Postgres' own text form, timestamps in
// UTC. It is hashed as-is for the row fingerprint, except bytea, which is
// decoded to its raw bytes. Display values are derived from the same text.

// Type OIDs from pg_type.
const (
	oidBool        = 16
	oidBytea       = 17
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidOID         = 26
	oidJSON        = 114
	oidFloat4      = 700
	oidFloat8      = 701
	oidTimestamp   = 1114
	oidTimestamptz = 1184
	oidNumeric     = 1700
	oidJSONB       = 3802
)

// arrayElem maps array type OIDs to their element type OIDs, for the
// element types whose values convert to something other than a string.
var arrayElem = map[uint32]uint32{
	1000: oidBool, 1001: oidBytea, 1005: oidInt2, 1007: oidInt4, 1016: oidInt8, 1028: oidOID,
	199: oidJSON, 3807: oidJSONB, 1021: oidFloat4, 1022: oidFloat8, 1231: oidNumeric,
	1115: oidTimestamp, 1185: oidTimestamptz,
	// arrays of text-like types; elements stay strings
	1009: 25, 1015: 1043, 1014: 1042, 2951: 2950, 1182: 1082, 1183: 1083, 1187: 1186, 1003: 19, 1002: 18,
}

// canonicalCell converts a raw text value to its fingerprint form.
func canonicalCell(oid uint32, raw []byte) connector.Cell {
	if raw == nil {
		return connector.NullCell()
	}
	if oid == oidBytea {
		if b, ok := decodeBytea(raw); ok {
			return connector.BytesCell(b)
		}
	}
	return connector.BytesCell(raw)
}

func decodeBytea(raw []byte) ([]byte, bool) {
	if !bytes.HasPrefix(raw, []byte(`\x`)) {
		return nil, false
	}
	b := make([]byte, hex.DecodedLen(len(raw)-2))
	if _, err := hex.Decode(b, raw[2:]); err != nil {
		return nil, false
	}
	return b, true
}

// displayValue converts a raw text value to a JSON-safe display value,
// truncating to maxBytes. The bool reports truncation.
func displayValue(oid uint32, raw []byte, maxBytes int) (any, bool) {
	if raw == nil {
		return nil, false
	}
	if elem, ok := arrayElem[oid]; ok {
		if maxBytes > 0 && len(raw) > maxBytes {
			return connector.TextValue(string(raw), maxBytes)
		}
		if arr, ok := parseArray(string(raw)); ok {
			return convertArray(arr, elem, maxBytes)
		}
		return connector.TextValue(string(raw), maxBytes)
	}
	return scalarValue(oid, string(raw), maxBytes)
}

func scalarValue(oid uint32, s string, maxBytes int) (any, bool) {
	switch oid {
	case oidBool:
		switch s {
		case "t":
			return true, false
		case "f":
			return false, false
		}
	case oidInt2, oidInt4, oidOID:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, false
		}
	case oidInt8:
		// Beyond 2^53 a JSON number loses precision in browsers.
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n <= 1<<53 && n >= -(1<<53) {
			return n, false
		}
		return s, false
	case oidBytea:
		if b, ok := decodeBytea([]byte(s)); ok {
			v, truncated := connector.BytesValue(b, maxBytes)
			return v, truncated
		}
	case oidJSON, oidJSONB:
		if maxBytes > 0 && len(s) > maxBytes {
			return connector.TextValue(s, maxBytes)
		}
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil && !dec.More() {
			return v, false
		}
	case oidTimestamptz:
		if t, err := time.Parse("2006-01-02 15:04:05.999999999Z07", s); err == nil {
			return t.UTC().Format(time.RFC3339Nano), false
		}
		if t, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", s); err == nil {
			return t.UTC().Format(time.RFC3339Nano), false
		}
	case oidTimestamp:
		if t, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
			return t.Format("2006-01-02T15:04:05.999999999"), false
		}
	}
	// Numerics, floats and everything else: the canonical text form.
	return connector.TextValue(s, maxBytes)
}

func convertArray(arr []any, elem uint32, maxBytes int) (any, bool) {
	out := make([]any, len(arr))
	truncated := false
	for i, e := range arr {
		switch x := e.(type) {
		case nil:
			out[i] = nil
		case []any:
			v, t := convertArray(x, elem, maxBytes)
			out[i], truncated = v, truncated || t
		case string:
			v, t := scalarValue(elem, x, maxBytes)
			out[i], truncated = v, truncated || t
		}
	}
	return out, truncated
}

// parseArray parses the text form of a Postgres array, e.g.
// {1,2,NULL,"a \"b\""} or {{1,2},{3,4}}. Elements are strings or nil;
// nested arrays are []any. Arrays with explicit bounds ([0:1]={...}) and
// non-comma delimiters are not handled (false).
func parseArray(s string) ([]any, bool) {
	p := &arrayParser{s: s}
	v, ok := p.array()
	if !ok || p.i != len(p.s) {
		return nil, false
	}
	return v, true
}

type arrayParser struct {
	s string
	i int
}

func (p *arrayParser) array() ([]any, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return nil, false
	}
	p.i++
	out := []any{}
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return out, true
	}
	for {
		if p.i >= len(p.s) {
			return nil, false
		}
		switch p.s[p.i] {
		case '{':
			sub, ok := p.array()
			if !ok {
				return nil, false
			}
			out = append(out, sub)
		case '"':
			str, ok := p.quoted()
			if !ok {
				return nil, false
			}
			out = append(out, str)
		default:
			start := p.i
			for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
				if p.s[p.i] == '"' || p.s[p.i] == '{' || p.s[p.i] == '\\' {
					return nil, false
				}
				p.i++
			}
			tok := p.s[start:p.i]
			if tok == "NULL" {
				out = append(out, nil)
			} else {
				out = append(out, tok)
			}
		}
		if p.i >= len(p.s) {
			return nil, false
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return out, true
		default:
			return nil, false
		}
	}
}

func (p *arrayParser) quoted() (string, bool) {
	p.i++ // opening quote
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\\':
			if p.i+1 >= len(p.s) {
				return "", false
			}
			b.WriteByte(p.s[p.i+1])
			p.i += 2
		case '"':
			p.i++
			return b.String(), true
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return "", false
}
