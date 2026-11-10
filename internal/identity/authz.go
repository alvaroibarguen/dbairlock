package identity

import (
	"errors"
	"slices"
)

// Action names something a principal wants to do. Every authorization
// decision in dbairlock is Can(principal, action, resource).
type Action string

// Actions.
const (
	ActionChangeSubmit Action = "change.submit"
	ActionChangeReview Action = "change.review"
	// ActionChangeAdminister covers acting on other people's changes:
	// cancelling them, triggering their execution, retrying a waiting one
	// now. It never allows approving anything (admins have no bypass).
	ActionChangeAdminister Action = "change.administer"
	ActionDatabaseManage   Action = "database.manage"
	ActionSettingsManage   Action = "settings.manage"
	ActionUserManage       Action = "user.manage"
	ActionAuditRead        Action = "audit.read"
)

// Actions returns every action in a stable order.
func Actions() []Action {
	return []Action{
		ActionChangeSubmit, ActionChangeReview, ActionChangeAdminister, ActionDatabaseManage,
		ActionSettingsManage, ActionUserManage, ActionAuditRead,
	}
}

// Resource is what an action is applied to: its type and id, for example
// {"connection", "<id>"} for a change that targets a connection. v1 rules do
// not look at it; per-database permissions will.
type Resource struct {
	Type string
	ID   string
}

// Instance is the resource for instance-wide actions (settings, users).
var Instance = Resource{Type: "instance"}

var (
	// ErrUnauthenticated means there is no signed-in principal.
	ErrUnauthenticated = errors.New("identity: not signed in")
	// ErrForbidden means the principal may not do this.
	ErrForbidden = errors.New("identity: not allowed")
)

// memberActions are what a member may do in v1. Admins may do everything.
var memberActions = []Action{ActionChangeSubmit, ActionChangeReview, ActionAuditRead}

// Can is the one authorization function. It returns nil if p may perform a
// on r, ErrUnauthenticated if p is not a signed-in principal, and
// ErrForbidden otherwise. Callers never compare roles themselves: role-based
// access control and per-database permissions will be added here without
// touching them.
func Can(p Principal, a Action, _ Resource) error {
	if p.UserID == "" {
		return ErrUnauthenticated
	}
	if !slices.Contains(Actions(), a) {
		return ErrForbidden // unknown actions are refused, never allowed
	}
	switch p.role {
	case RoleAdmin:
		return nil
	case RoleMember:
		if slices.Contains(memberActions, a) {
			return nil
		}
	}
	return ErrForbidden
}

// Permissions lists the actions p may perform on instance-wide resources,
// for the dashboard to decide what to show. The server still checks Can on
// every request.
func Permissions(p Principal) []Action {
	var out []Action
	for _, a := range Actions() {
		if Can(p, a, Instance) == nil {
			out = append(out, a)
		}
	}
	return out
}
