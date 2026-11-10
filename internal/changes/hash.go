package changes

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// The two hashes an approval is bound to. DO NOT CHANGE THE ENCODINGS: they
// are stored with every change and review; a different encoding would make
// every open approval stop counting. A new encoding needs a new prefix.

const (
	sqlHashVersion     = "dbairlock/sql/v1\x00"
	previewHashVersion = "dbairlock/preview/v1\x00"
)

// SQLHash identifies exactly what will run: every statement's text, in
// order, length-prefixed so no two lists share an encoding.
func SQLHash(statements []string) string {
	h := sha256.New()
	h.Write([]byte(sqlHashVersion))
	h.Write(u64(uint64(len(statements))))
	for _, s := range statements {
		h.Write(u64(uint64(len(s))))
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// expectation is what the dry run recorded for one statement.
type expectation struct {
	Rows        int64
	Fingerprint string
}

// previewHash identifies the data a reviewer saw: every statement's expected
// row count and row fingerprint, in order.
func previewHash(exps []expectation) string {
	h := sha256.New()
	h.Write([]byte(previewHashVersion))
	h.Write(u64(uint64(len(exps))))
	for _, e := range exps {
		h.Write(u64(uint64(max(e.Rows, 0)))) // row counts are never negative
		h.Write(u64(uint64(len(e.Fingerprint))))
		h.Write([]byte(e.Fingerprint))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
