package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

type fakeConnector struct{ id string }

func (f fakeConnector) Info() Info                                       { return Info{ID: f.id} }
func (fakeConnector) ConfigFields() []Field                              { return nil }
func (fakeConnector) ValidateConfig(c Config) (Config, error)            { return c, nil }
func (fakeConnector) Validate(Config, string, Policy) (Statement, error) { return Statement{}, nil }
func (fakeConnector) Open(context.Context, Config, []byte) (Conn, error) {
	return nil, errors.New("not implemented")
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fakeConnector{"b"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeConnector{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeConnector{"a"}); err == nil {
		t.Error("duplicate id accepted")
	}
	if err := r.Register(fakeConnector{" "}); err == nil {
		t.Error("empty id accepted")
	}
	if _, ok := r.Get("a"); !ok {
		t.Error("Get(a) not found")
	}
	if _, ok := r.Get("zzz"); ok {
		t.Error("Get(zzz) found")
	}
	list := r.List()
	if len(list) != 2 || list[0].Info().ID != "a" || list[1].Info().ID != "b" {
		t.Errorf("List = %v", list)
	}
}

func TestErrors(t *testing.T) {
	err := Unavailable(errors.New("dial tcp: refused"))
	if !errors.Is(err, ErrUnavailable) {
		t.Error("Unavailable does not wrap ErrUnavailable")
	}
	se := &StatementError{Index: 1, Err: fmt.Errorf("%w: 5 rows", ErrTooManyRows)}
	if !errors.Is(se, ErrTooManyRows) || se.Error() != "statement 2: statement affects too many rows: 5 rows" {
		t.Errorf("StatementError = %q", se.Error())
	}
	d := &DriftError{Statement: 0, Kind: DriftCount, Expected: "1", Actual: "3"}
	if d.Error() != "statement 1: drift (count): expected 1, found 3" {
		t.Errorf("DriftError = %q", d.Error())
	}
	var rej *RejectedError
	if !errors.As(Reject(RejectFunction, 4, "function %s", "md5"), &rej) || rej.Position != 4 || rej.Reason != "function md5" {
		t.Errorf("Reject = %+v", rej)
	}
}

func TestPolicyAllows(t *testing.T) {
	if !(Policy{}).Allows("lower") || (Policy{}).Allows("md5") {
		t.Error("default allowlist wrong")
	}
	if (Policy{AllowedFunctions: []string{}}).Allows("lower") {
		t.Error("empty allowlist allows lower")
	}
	if !(Policy{AllowedFunctions: []string{"md5"}}).Allows("md5") {
		t.Error("override does not allow md5")
	}
}

func TestTruncateText(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		{"hello", 10, "hello", false},
		{"hello", 0, "hello", false},
		{"hello", 3, "hel", true},
		{"héllo", 2, "h", true}, // never splits a rune
		{"日本", 4, "日", true},
	}
	for _, c := range cases {
		got, cut := TruncateText(c.in, c.max)
		if got != c.want || cut != c.cut {
			t.Errorf("TruncateText(%q, %d) = %q, %v", c.in, c.max, got, cut)
		}
	}
	v, cut := TextValue("abcdef", 3)
	b, _ := json.Marshal(v)
	if !cut || string(b) != `{"type":"text","value":"abc","size":6,"truncated":true}` {
		t.Errorf("TextValue = %s", b)
	}
	bv, cut := BytesValue([]byte{0, 1, 2, 3}, 2)
	b, _ = json.Marshal(bv)
	if !cut || string(b) != `{"type":"bytes","value":"AAE=","size":4,"truncated":true}` {
		t.Errorf("BytesValue = %s", b)
	}
}

func TestCheckAddr(t *testing.T) {
	for addr, ok := range map[string]bool{
		"169.254.169.254":    false,
		"fe80::1":            false,
		"::ffff:169.254.1.1": false,
		"0.0.0.0":            false,
		"10.0.0.5":           true,
		"192.168.1.1":        true,
		"127.0.0.1":          true,
		"2001:db8::1":        true,
	} {
		err := CheckAddr(netip.MustParseAddr(addr))
		if (err == nil) != ok {
			t.Errorf("CheckAddr(%s) = %v", addr, err)
		}
	}
	if CheckHost("db.internal") != nil || CheckHost("169.254.169.254") == nil {
		t.Error("CheckHost")
	}
}
