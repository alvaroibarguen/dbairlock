package audit

import (
	"encoding/hex"
	"strings"
	"testing"
)

// These golden values pin the bytes every stored hash depends on. If one of
// these tests fails, the change broke verification of existing databases:
// revert it, do not update the golden value.

var goldenRecord = record{
	Seq:         42,
	ID:          "01937f6e-8a00-7000-8000-000000000001",
	OccurredAt:  1767225600123,
	ActorUserID: "u-1",
	ActorEmail:  "ana@example.com",
	Via:         "web",
	APITokenID:  "",
	IP:          "10.0.0.7",
	Action:      "change.submitted",
	SubjectType: "change",
	SubjectID:   "c-9",
	Payload:     `{ "z": 1, "a": {"y": [3, 2.50, "<&>"], "b": null}, "é": "ü\u2028\"" }`,
}

const goldenCanonical = `{"seq":42,"id":"01937f6e-8a00-7000-8000-000000000001","occurred_at":1767225600123,` +
	`"actor_user_id":"u-1","actor_email":"ana@example.com","via":"web","api_token_id":"","ip":"10.0.0.7",` +
	`"action":"change.submitted","subject_type":"change","subject_id":"c-9",` +
	`"payload":{"a":{"b":null,"y":[3,2.50,"<&>"]},"z":1,"é":"ü\u2028\""}}`

func TestCanonicalJSONGolden(t *testing.T) {
	got, err := canonicalJSON(goldenRecord)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goldenCanonical {
		t.Fatalf("canonical JSON changed:\n got %s\nwant %s", got, goldenCanonical)
	}
}

func TestChainHashGolden(t *testing.T) {
	const want = "783c0a5ece0b6ba6e310feabe73be4e3d6d2619ca5c18cf0350dd7b98b1dcc6f"
	got, err := chainHash(make([]byte, hashSize), goldenRecord)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != want {
		t.Fatalf("chain hash changed: got %x want %s", got, want)
	}
}

func TestCanonicalPayloadGolden(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{}`, `{}`},
		{` { } `, `{}`},
		{`{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{`{"n":[{"d":1,"c":{"f":0,"e":0}}]}`, `{"n":[{"c":{"e":0,"f":0},"d":1}]}`},
		{`{"num":1e3,"neg":-0.10,"big":123456789012345678901234567890}`, `{"big":123456789012345678901234567890,"neg":-0.10,"num":1e3}`},
		{`{"html":"<script>&</script>"}`, `{"html":"<script>&</script>"}`},
		{`{"ctl":"tab\tnl\n"}`, `{"ctl":"tab\tnl\n"}`},
		{`{"u":"\u00e9"}`, `{"u":"é"}`},
		{`{"t":true,"f":false,"z":null}`, `{"f":false,"t":true,"z":null}`},
	}
	for _, tc := range cases {
		got, err := canonicalPayload([]byte(tc.in))
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.in, got, tc.want)
		}
		again, err := canonicalPayload(got)
		if err != nil || string(again) != string(got) {
			t.Errorf("%s: not idempotent: %s", tc.in, again)
		}
	}
	for _, bad := range []string{``, `[]`, `"s"`, `1`, `null`, `{"a":1} {}`, `{"a":}`} {
		if _, err := canonicalPayload([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPreparePayloadTruncates(t *testing.T) {
	long := strings.Repeat("é", MaxPayloadString) // 2 bytes per rune
	got, err := preparePayload(map[string]any{
		"short":  "ok",
		"long":   long,
		"nested": []any{map[string]any{"deep": long}},
	})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := decodeObject([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	marker := "...[truncated: 16384 bytes]"
	s := obj["long"].(string)
	if !strings.HasSuffix(s, marker) || len(s) != MaxPayloadString+len(marker) {
		t.Fatalf("long: len %d, suffix %q", len(s), s[len(s)-40:])
	}
	deep := obj["nested"].([]any)[0].(map[string]any)["deep"].(string)
	if deep != s {
		t.Fatal("nested string not truncated the same way")
	}
	if obj["short"] != "ok" {
		t.Fatal("short string changed")
	}

	if got, err := preparePayload(nil); err != nil || got != "{}" {
		t.Fatalf("nil payload = %q, %v", got, err)
	}
	if _, err := preparePayload([]int{1}); err == nil {
		t.Fatal("array payload accepted")
	}
}
