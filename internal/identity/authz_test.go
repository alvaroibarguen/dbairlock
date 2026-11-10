package identity

import (
	"errors"
	"strings"
	"testing"
)

// TestCanTable covers every (role, action) pair of the v1 rule table, plus
// an anonymous principal and an unknown action.
func TestCanTable(t *testing.T) {
	want := map[Role]map[Action]bool{
		RoleAdmin: {
			ActionChangeSubmit: true, ActionChangeReview: true, ActionChangeAdminister: true, ActionDatabaseManage: true,
			ActionSettingsManage: true, ActionUserManage: true, ActionAuditRead: true,
		},
		RoleMember: {
			ActionChangeSubmit: true, ActionChangeReview: true, ActionChangeAdminister: false, ActionDatabaseManage: false,
			ActionSettingsManage: false, ActionUserManage: false, ActionAuditRead: true,
		},
	}
	for role, actions := range want {
		if len(actions) != len(Actions()) {
			t.Fatalf("table for %s misses actions", role)
		}
		p := Principal{UserID: "u1", role: role}
		for _, a := range Actions() {
			err := Can(p, a, Resource{Type: "connection", ID: "c1"})
			if allowed := err == nil; allowed != actions[a] {
				t.Errorf("Can(%s, %s) = %v, want allowed=%v", role, a, err, actions[a])
			}
			if err != nil && !errors.Is(err, ErrForbidden) {
				t.Errorf("Can(%s, %s) = %v, want ErrForbidden", role, a, err)
			}
		}
		if err := Can(p, Action("drop.everything"), Instance); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: unknown action = %v", role, err)
		}
	}
	for _, a := range Actions() {
		if err := Can(Principal{}, a, Instance); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("anonymous %s = %v", a, err)
		}
		// A principal with no valid role (never produced by identity) is refused.
		if err := Can(Principal{UserID: "u1", role: "owner"}, a, Instance); !errors.Is(err, ErrForbidden) {
			t.Errorf("unknown role %s = %v", a, err)
		}
	}
	if got := Permissions(Principal{UserID: "u", role: RoleMember}); len(got) != 3 {
		t.Errorf("member permissions = %v", got)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                         "/",
		"/changes/42?tab=rows":     "/changes/42?tab=rows",
		"//evil.example.com/x":     "/",
		"https://evil.example.com": "/",
		"/\\evil.example.com":      "/",
		"javascript:alert(1)":      "/",
		"changes":                  "/",
		"/ok\r\nSet-Cookie: x=1":   "/",
	}
	for in, want := range cases {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		s := newTokenSecret()
		if len(s) != len(TokenPrefix)+tokenBodyLen || s[:len(TokenPrefix)] != TokenPrefix || seen[s] {
			t.Fatalf("bad token %q", s)
		}
		for _, r := range s[len(TokenPrefix):] {
			if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
				t.Fatalf("not base62: %q", s)
			}
		}
		seen[s] = true
	}
}
