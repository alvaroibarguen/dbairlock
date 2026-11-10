package audit

import (
	"strings"
	"testing"
)

func TestPrefixUpperBound(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"change.", "change/", true},
		{"a", "b", true},
		{"a\xff", "b", true},
		{"\xff\xff", "", false},
	} {
		got, ok := prefixUpperBound(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("prefixUpperBound(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	// Everything with the prefix is inside [prefix, bound), and the
	// neighbours that only look similar are not.
	hi, _ := prefixUpperBound("change.")
	for _, s := range []string{"change.", "change.approved", "change.\xff"} {
		if !strings.HasPrefix(s, "change.") || s < "change." || s >= hi {
			t.Errorf("%q outside the range", s)
		}
	}
	for _, s := range []string{"change", "changes.x", "change/"} {
		if s >= "change." && s < hi {
			t.Errorf("%q inside the range", s)
		}
	}
}
