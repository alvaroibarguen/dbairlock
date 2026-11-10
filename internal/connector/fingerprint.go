package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
)

// The row fingerprint (PLAN.md 5.4) is the reviewer's approval in data form:
// a SHA-256 over exactly the values the reviewer saw, for every matched row.
// Both connectors compute it here so they cannot diverge; each connector
// supplies only the conversion of its values to canonical bytes.
//
// DO NOT CHANGE THE ENCODING. Fingerprints are stored with approved changes
// and compared at execution; a different encoding would make every pending
// change drift. A new encoding needs a new version prefix.
//
// Encoding (all integers are unsigned 64-bit big-endian):
//
//	fingerprint = hex(SHA-256(fingerprintVersion || count(rows) || row...))
//	row         = cells(key) || cells(values)
//	cells(cs)   = count(cs) || cell...
//	cell        = 0x00                      for NULL
//	            | 0x01 || len(data) || data  otherwise
//
// Rows are sorted by their encoded key, then by their encoded values, as raw
// bytes. Sorting is done here, never by trusting the database's collation.
// For a table without a primary key the key is empty and the values are the
// whole row, so rows sort by their encoded contents.

const fingerprintVersion = "dbairlock/row-fingerprint/v1\x00"

// Cell is one value in canonical form: complete (never truncated) and
// independent of session settings. Timestamps are in UTC, numerics are in
// the database's canonical text form, binary values are raw bytes.
type Cell struct {
	Null bool
	Data []byte
}

// NullCell is the SQL NULL.
func NullCell() Cell { return Cell{Null: true} }

// BytesCell is a non-null value.
func BytesCell(b []byte) Cell { return Cell{Data: b} }

// FingerprintRow is one matched row as it enters the fingerprint.
type FingerprintRow struct {
	// Key holds the primary key values, in key column order. Empty for a
	// table without a primary key.
	Key []Cell
	// Values holds, for an UPDATE on a keyed table, the current values of
	// the columns the statement sets (in statement order); for a DELETE on
	// a keyed table, nothing; for a table without a primary key, every
	// column of the row in table order.
	Values []Cell
}

// Fingerprint returns the hex SHA-256 row fingerprint of rows. The result
// does not depend on the order of rows.
func Fingerprint(rows []FingerprintRow) string {
	type encoded struct{ key, values []byte }
	enc := make([]encoded, len(rows))
	for i, r := range rows {
		enc[i] = encoded{key: encodeCells(r.Key), values: encodeCells(r.Values)}
	}
	slices.SortFunc(enc, func(a, b encoded) int {
		if c := bytes.Compare(a.key, b.key); c != 0 {
			return c
		}
		return bytes.Compare(a.values, b.values)
	})
	h := sha256.New()
	h.Write([]byte(fingerprintVersion))
	h.Write(u64(uint64(len(enc))))
	for _, e := range enc {
		h.Write(e.key)
		h.Write(e.values)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func encodeCells(cells []Cell) []byte {
	n := 8
	for _, c := range cells {
		n += 1 + 8 + len(c.Data)
	}
	out := make([]byte, 0, n)
	out = append(out, u64(uint64(len(cells)))...)
	for _, c := range cells {
		if c.Null {
			out = append(out, 0x00)
			continue
		}
		out = append(out, 0x01)
		out = append(out, u64(uint64(len(c.Data)))...)
		out = append(out, c.Data...)
	}
	return out
}

func u64(v uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, v)
}
