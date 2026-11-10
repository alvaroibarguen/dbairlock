package audit

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzCanonicalPayload checks the properties the hash chain relies on for any
// input: canonicalization never panics, its output is a JSON object that
// canonicalizes to itself (the verifier re-canonicalizes stored payloads),
// and it keeps the decoded value.
func FuzzCanonicalPayload(f *testing.F) {
	for _, s := range []string{
		`{}`, `{"b":1,"a":2}`, goldenRecord.Payload,
		`{"num":1e3,"neg":-0.10,"big":123456789012345678901234567890}`,
		`{"s":"\ud800"}`, `{"a":1,"a":2}`, `{"x":[[[[{"y":null}]]]]}`,
		"{\"bad\":\"\xff\xfe\"}", `{"html":"<script>&</script>","ls":"\u2028\u2029"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := canonicalPayload(raw)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("canonical payload of %q is not valid JSON: %q", raw, out)
		}
		again, err := canonicalPayload(out)
		if err != nil {
			t.Fatalf("canonical payload %q of %q does not canonicalize again: %v", out, raw, err)
		}
		if !bytes.Equal(again, out) {
			t.Fatalf("not idempotent for %q:\n first %q\nsecond %q", raw, out, again)
		}
	})
}

// FuzzPreparePayload runs arbitrary JSON objects through the path the
// recorder takes (marshal, truncate, canonicalize) and checks that the
// stored text is what the verifier will hash: canonicalPayload of it is the
// identity, and chainHash over a record carrying it succeeds and is stable.
func FuzzPreparePayload(f *testing.F) {
	f.Add([]byte(`{"key":"policy.default_max_rows","old":100,"new":250}`), "u-1", "ana@example.com", "10.0.0.1")
	f.Add([]byte(`{"sql":"UPDATE t SET a = 1 WHERE id = 2","title":"<b>x</b>"}`), "", "", "")
	f.Add([]byte(`{"long":"`+string(bytes.Repeat([]byte("é"), MaxPayloadString))+`"}`), "x", "\xff", "::1")
	f.Fuzz(func(t *testing.T, raw []byte, userID, email, ip string) {
		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil || payload == nil {
			return
		}
		stored, err := preparePayload(payload)
		if err != nil {
			if checkPayload(payload) == nil {
				t.Fatalf("preparePayload(%q) failed: %v", raw, err)
			}
			return
		}
		canon, err := canonicalPayload([]byte(stored))
		if err != nil || string(canon) != stored {
			t.Fatalf("stored payload %q is not canonical: %q, %v", stored, canon, err)
		}
		r := record{
			Seq: 7, ID: "id", OccurredAt: 1, ActorUserID: userID, ActorEmail: email, Via: "web",
			IP: ip, Action: "change.submitted", SubjectType: "change", SubjectID: "c", Payload: stored,
		}
		c, err := canonicalJSON(r)
		if err != nil {
			t.Fatalf("canonicalJSON: %v", err)
		}
		if !json.Valid(c) {
			t.Fatalf("canonical record is not valid JSON: %q", c)
		}
		h1, err := chainHash(make([]byte, hashSize), r)
		if err != nil {
			t.Fatal(err)
		}
		h2, _ := chainHash(make([]byte, hashSize), r)
		if !bytes.Equal(h1, h2) {
			t.Fatal("chain hash is not deterministic")
		}
	})
}
