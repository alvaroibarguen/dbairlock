package audit

import (
	"context"
	"errors"
	"fmt"
)

// Via is the channel through which an actor made a change. Values match the
// CHECK constraint on audit_events.via.
type Via string

// Channels.
const (
	ViaWeb    Via = "web"
	ViaMCP    Via = "mcp"
	ViaCLI    Via = "cli"
	ViaSystem Via = "system"
)

// Actor is who made a change. The auth middleware (Phase 4) puts one in the
// request context; background work uses SystemActor explicitly.
type Actor struct {
	UserID     string
	Email      string
	Via        Via
	APITokenID string
	IP         string
}

// SystemActor is the actor for work the server does on its own, such as the
// executor. Use it explicitly with WithActor; it is never a default.
var SystemActor = Actor{Via: ViaSystem}

// ErrNoActor means Record was called with a context that carries no Actor.
var ErrNoActor = errors.New("audit: no actor in context")

type actorKey struct{}

// WithActor returns a context that carries a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the Actor carried by ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Label is a short human-readable name for the actor, stored as "set by" or
// "updated by" next to records it changed: the email if known, otherwise the
// user id, otherwise the channel ("cli", "system").
func (a Actor) Label() string {
	switch {
	case a.Email != "":
		return a.Email
	case a.UserID != "":
		return a.UserID
	default:
		return string(a.Via)
	}
}

func (a Actor) validate() error {
	switch a.Via {
	case ViaWeb, ViaMCP:
		if a.UserID == "" {
			return fmt.Errorf("audit: actor via %s needs a user id", a.Via)
		}
	case ViaCLI, ViaSystem:
	default:
		return fmt.Errorf("audit: invalid actor channel %q", a.Via)
	}
	return nil
}
