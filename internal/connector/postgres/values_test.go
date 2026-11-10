package postgres

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

func TestDisplayValue(t *testing.T) {
	cases := []struct {
		oid  uint32
		raw  []byte
		max  int
		want string // JSON
		cut  bool
	}{
		{oidBool, []byte("t"), 0, `true`, false},
		{oidInt4, []byte("-42"), 0, `-42`, false},
		{oidInt8, []byte("9007199254740992"), 0, `9007199254740992`, false},
		{oidInt8, []byte("9007199254740993"), 0, `"9007199254740993"`, false},
		{oidNumeric, []byte("1.50"), 0, `"1.50"`, false},
		{oidFloat8, []byte("NaN"), 0, `"NaN"`, false},
		{oidBytea, []byte(`\x00ff`), 0, `{"type":"bytes","value":"AP8=","size":2}`, false},
		{oidJSONB, []byte(`{"a": 12345678901234567890}`), 0, `{"a":12345678901234567890}`, false},
		{oidJSON, []byte(`{"a": 1}`), 3, `{"type":"text","value":"{\"a","size":8,"truncated":true}`, true},
		{oidTimestamptz, []byte("2024-03-04 03:06:07.5+00"), 0, `"2024-03-04T03:06:07.5Z"`, false},
		{oidTimestamptz, []byte("infinity"), 0, `"infinity"`, false},
		{oidTimestamp, []byte("2024-01-02 03:04:05"), 0, `"2024-01-02T03:04:05"`, false},
		{1082, []byte("2024-01-02"), 0, `"2024-01-02"`, false},
		{25, []byte("hello world"), 5, `{"type":"text","value":"hello","size":11,"truncated":true}`, true},
		{1007, []byte("{1,NULL,3}"), 0, `[1,null,3]`, false},
		{1009, []byte(`{"a b","q\"x",NULL,plain}`), 0, `["a b","q\"x",null,"plain"]`, false},
		{1007, []byte("{{1,2},{3,4}}"), 0, `[[1,2],[3,4]]`, false},
		{1007, []byte("[0:1]={1,2}"), 0, `"[0:1]={1,2}"`, false},
		{1009, []byte("{}"), 0, `[]`, false},
		{25, nil, 0, `null`, false},
	}
	for _, c := range cases {
		v, cut := displayValue(c.oid, c.raw, c.max)
		got, err := json.Marshal(v)
		if err != nil {
			t.Errorf("%d %q: %v", c.oid, c.raw, err)
			continue
		}
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal([]byte(c.want), &b)
		if !reflect.DeepEqual(a, b) || cut != c.cut {
			t.Errorf("displayValue(%d, %q) = %s, %v; want %s, %v", c.oid, c.raw, got, cut, c.want, c.cut)
		}
	}
}

func TestCanonicalCell(t *testing.T) {
	if c := canonicalCell(oidBytea, []byte(`\x00ff`)); c.Null || string(c.Data) != "\x00\xff" {
		t.Errorf("bytea = %+v", c)
	}
	if c := canonicalCell(25, nil); !c.Null {
		t.Error("nil is not NULL")
	}
	if c := canonicalCell(25, []byte{}); c.Null || len(c.Data) != 0 {
		t.Error("empty string is not distinct from NULL")
	}
	// Truncation never applies to the canonical value.
	long := make([]byte, 10000)
	if c := canonicalCell(25, long); len(c.Data) != 10000 {
		t.Error("canonical value truncated")
	}
}

func TestTextArray(t *testing.T) {
	if got := textArray([]string{"1", `a"b\c`}); got != `{"1","a\"b\\c"}` {
		t.Errorf("textArray = %s", got)
	}
	arr, ok := parseArray(textArray([]string{"x,y", `"`, `\`}))
	if !ok || !reflect.DeepEqual(arr, []any{"x,y", `"`, `\`}) {
		t.Errorf("round trip = %v, %v", arr, ok)
	}
}

func TestLabel(t *testing.T) {
	if got := label("dbairlock change #12"); got != "dbairlock change #12" {
		t.Errorf("label = %q", got)
	}
	if got := label("ünï\x00"); got != "??n??" && len(got) > 63 {
		t.Errorf("label = %q", got)
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	if got := label(string(long)); len(got) != 63 {
		t.Errorf("label length = %d", len(got))
	}
	if label("") != "dbairlock" {
		t.Error("empty label")
	}
}

func TestParseServerInfo(t *testing.T) {
	for num, want := range map[string]connector.ServerInfo{
		"140024": {Product: "PostgreSQL", Version: "v", Major: 14, Supported: true},
		"180006": {Product: "PostgreSQL", Version: "v", Major: 18, Supported: true},
		"130020": {Product: "PostgreSQL", Version: "v", Major: 13, Supported: false},
		"190000": {Product: "PostgreSQL", Version: "v", Major: 19, Supported: false},
	} {
		got, err := parseServerInfo(num, "v")
		if err != nil || got != want {
			t.Errorf("parseServerInfo(%s) = %+v, %v", num, got, err)
		}
	}
	if _, err := parseServerInfo("x", "v"); err == nil {
		t.Error("bad version accepted")
	}
}

func TestValidateConfig(t *testing.T) {
	c := New()
	got, err := c.ValidateConfig(connector.Config{"host": "db", "port": float64(5433), "database": "app", "username": "u"})
	if err != nil {
		t.Fatal(err)
	}
	want := connector.Config{"host": "db", "port": 5433, "database": "app", "username": "u",
		"default_schema": "public", "tls_mode": "verify-full"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ValidateConfig = %v, want %v", got, want)
	}
	for name, cfg := range map[string]connector.Config{
		"missing host":    {"database": "app", "username": "u"},
		"metadata host":   {"host": "169.254.169.254", "database": "app", "username": "u"},
		"bad port":        {"host": "db", "port": 70000, "database": "app", "username": "u"},
		"fractional port": {"host": "db", "port": 1.5, "database": "app", "username": "u"},
		"password":        {"host": "db", "database": "app", "username": "u", "password": "x"},
		"unknown field":   {"host": "db", "database": "app", "username": "u", "sslmode": "x"},
		"bad tls":         {"host": "db", "database": "app", "username": "u", "tls_mode": "prefer"},
		"system schema":   {"host": "db", "database": "app", "username": "u", "default_schema": "pg_catalog"},
		"bad ca":          {"host": "db", "database": "app", "username": "u", "ca_certificate": "nope"},
		"host injection":  {"host": "db sslmode=disable", "database": "app", "username": "u"},
	} {
		if _, err := c.ValidateConfig(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Every config field is declared; the secret is a secret field.
	var secret int
	for _, f := range c.ConfigFields() {
		if f.Type == connector.FieldSecret {
			secret++
		}
	}
	if secret != 1 {
		t.Errorf("%d secret fields", secret)
	}
}
