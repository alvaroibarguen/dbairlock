package settings

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// FuzzDefinitionParse feeds arbitrary JSON to every setting's validator. A
// value must either be rejected with a user-facing message or be accepted in
// a normalized form that is itself accepted unchanged: the stored value is
// parsed again on every start, so a value that validates once but not twice
// would stop the instance from booting.
func FuzzDefinitionParse(f *testing.F) {
	seeds := []string{
		`"dbairlock"`, `""`, `"  x  "`, `12`, `-1`, `1e3`, `true`, `null`, `[]`, `{}`,
		`"https://dbairlock.example.com/"`, `"http://127.0.0.1:9999/"`, `"https://accounts.google.com"`,
		`["10.0.0.0/8","::1","10.0.0.1/8"]`, `["Example.COM"," a.b "]`, `["change.failed","change.submitted"]`,
		`"https://u:p@h/?q#f"`, `"slack"`, `"a\u0000b"`, `"http://[::1]/"`, `"http://h/%2F/"`,
	}
	for i := range definitions {
		for _, s := range seeds {
			f.Add(uint8(i), []byte(s))
		}
	}
	f.Fuzz(func(t *testing.T, which uint8, raw []byte) {
		d := definitions[int(which)%len(definitions)]
		v, err := d.parse(json.RawMessage(raw))
		if err != nil {
			var msg invalid
			if !errors.As(err, &msg) {
				t.Fatalf("%s: parse(%q) returned a non-validation error: %v", d.Key, raw, err)
			}
			if msg == "" {
				t.Fatalf("%s: parse(%q) rejected without a message", d.Key, raw)
			}
			return
		}
		stored, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: accepted value %#v does not marshal: %v", d.Key, v, err)
		}
		again, err := d.parse(json.RawMessage(stored))
		if err != nil {
			t.Fatalf("%s: accepted %q as %s, which is then rejected: %v", d.Key, raw, stored, err)
		}
		if !reflect.DeepEqual(again, v) {
			t.Fatalf("%s: normalization of %q is not stable: %#v then %#v", d.Key, raw, v, again)
		}
		if reflect.TypeOf(v) != reflect.TypeOf(d.Default) {
			t.Fatalf("%s: accepted value has type %T, the default %T", d.Key, v, d.Default)
		}
	})
}
