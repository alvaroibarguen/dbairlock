package executor

import (
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// The executor's behaviour against real targets is tested in internal/app
// (changes_test.go); this covers the wording people see.
func TestDriftMessage(t *testing.T) {
	cases := []struct {
		kind             connector.DriftKind
		expected, actual string
		want             string
	}{
		{connector.DriftCount, "1", "3", "This change was approved for 1 row but would now affect 3 rows, so nothing was changed."},
		{connector.DriftCount, "2", "more than 100", "This change was approved for 2 rows but would now affect more than 100 rows, so nothing was changed."},
		{connector.DriftRows, "a", "b", "The rows this change would affect, or the values it would overwrite, are no longer the ones that were reviewed, so nothing was changed."},
	}
	for _, c := range cases {
		if got := DriftMessage(c.kind, c.expected, c.actual); got != c.want {
			t.Errorf("DriftMessage(%s, %s, %s) = %q", c.kind, c.expected, c.actual, got)
		}
	}
}
