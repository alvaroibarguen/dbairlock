package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func cells(vals ...any) []Cell {
	out := make([]Cell, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case nil:
			out[i] = NullCell()
		case string:
			out[i] = BytesCell([]byte(x))
		case []byte:
			out[i] = BytesCell(x)
		}
	}
	return out
}

// TestFingerprintEncoding pins the canonical encoding byte for byte. If
// this test fails, stored fingerprints no longer match: do not "fix" the
// expected bytes; introduce a new version instead.
func TestFingerprintEncoding(t *testing.T) {
	got := encodeCells(cells("ab", nil, ""))
	want := []byte{
		0, 0, 0, 0, 0, 0, 0, 3, // three cells
		1, 0, 0, 0, 0, 0, 0, 0, 2, 'a', 'b', // "ab"
		0,                         // NULL
		1, 0, 0, 0, 0, 0, 0, 0, 0, // ""
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeCells = %v, want %v", got, want)
	}

	// The whole fingerprint of one row, computed by hand.
	h := sha256.New()
	h.Write([]byte("dbairlock/row-fingerprint/v1\x00"))
	h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1})                                 // one row
	h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, '7'}) // key ("7")
	h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1, 0})                              // values (NULL)
	wantFP := hex.EncodeToString(h.Sum(nil))
	if fp := Fingerprint([]FingerprintRow{{Key: cells("7"), Values: cells(nil)}}); fp != wantFP {
		t.Fatalf("Fingerprint = %s, want %s", fp, wantFP)
	}
}

// TestFingerprintGolden pins fingerprints of fixed inputs.
func TestFingerprintGolden(t *testing.T) {
	cases := []struct {
		name string
		rows []FingerprintRow
		want string
	}{
		{"no rows", nil, goldenEmpty},
		{"keyed update", []FingerprintRow{
			{Key: cells("1"), Values: cells("Ann")},
			{Key: cells("2"), Values: cells("Bob")},
		}, goldenKeyed},
		{"no key", []FingerprintRow{
			{Values: cells("1", "created")},
			{Values: cells("1", "created")},
			{Values: cells("2", nil)},
		}, goldenNoKey},
		{"binary", []FingerprintRow{{Key: cells([]byte{0, 0xff}), Values: cells([]byte{})}}, goldenBinary},
	}
	for _, c := range cases {
		if got := Fingerprint(c.rows); got != c.want {
			t.Errorf("%s: Fingerprint = %s, want %s", c.name, got, c.want)
		}
	}
}

const (
	goldenEmpty  = "b4170e94e9a154f5b14326f116356b9ec262e4051bd43b337b9fcf05cad5be4d"
	goldenKeyed  = "172caa2c121186a8f8287f1976aff478c979ce5899d9a4086d0016feb32fd8fd"
	goldenNoKey  = "56d428e7c4aab2a07f0dc4385502e812fc074d43159aeac745203b322cb13672"
	goldenBinary = "50efb7736623f939656467f17b36a7b95c76481ed0d5fb658701dad1acdeccaf"
)

func TestFingerprintProperties(t *testing.T) {
	a := []FingerprintRow{{Key: cells("1"), Values: cells("x")}, {Key: cells("2"), Values: cells("y")}}
	b := []FingerprintRow{a[1], a[0]}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("fingerprint depends on row order")
	}
	distinct := map[string]string{}
	for name, rows := range map[string][]FingerprintRow{
		"ab|c":          {{Values: cells("ab", "c")}},
		"a|bc":          {{Values: cells("a", "bc")}},
		"null":          {{Values: cells(nil)}},
		"empty":         {{Values: cells("")}},
		"no cells":      {{}},
		"key 1 value 2": {{Key: cells("1"), Values: cells("2")}},
		"key 12":        {{Key: cells("1", "2")}},
		"values 12":     {{Values: cells("1", "2")}},
		"two rows":      {{Values: cells("1")}, {Values: cells("1")}},
		"one row":       {{Values: cells("1")}},
		"no rows":       nil,
	} {
		fp := Fingerprint(rows)
		if other, dup := distinct[fp]; dup {
			t.Errorf("%q and %q have the same fingerprint", name, other)
		}
		distinct[fp] = name
	}
}
