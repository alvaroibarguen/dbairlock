package changes

import (
	"context"
	"fmt"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Notifier queues a notification inside the caller's transaction, so it is
// sent only if the state change commits. app wires the notify module in.
type Notifier interface {
	Notify(ctx context.Context, tx *store.Tx, n Notice) error
}

// Notice is a change event worth telling people about. It never carries
// SQL, row data or reasons.
type Notice struct {
	// Event is change.submitted, change.approved, change.rejected,
	// change.executed or change.failed.
	Event    string
	Number   int64
	Title    string
	Database string // the connection's name
	Author   string // email
	Reviewer string // who approved or rejected; empty otherwise
	Status   Status
	// URL opens the change in the dashboard; empty when instance.public_url
	// is not set.
	URL string
}

// noticeEvents maps the audit actions that notify to their notice event.
var noticeEvents = map[audit.Action]string{
	audit.ChangeSubmitted:      "change.submitted",
	audit.ChangeApproved:       "change.approved",
	audit.ChangeRejected:       "change.rejected",
	audit.ExecutionSucceeded:   "change.executed",
	audit.ExecutionFailed:      "change.failed",
	audit.ExecutionInterrupted: "change.failed",
}

// notify queues the notice for action on change id, if action is one that
// notifies. It reads the change inside tx, so it reports the state being
// committed.
func (s *Service) notify(ctx context.Context, tx *store.Tx, action audit.Action, id string) error {
	event, ok := noticeEvents[action]
	if !ok || s.opts.Notifier == nil {
		return nil
	}
	row, err := changesdb.New(tx).GetChange(ctx, id)
	if err != nil {
		return fmt.Errorf("changes: read change for notification: %w", err)
	}
	n := Notice{Event: event, Number: row.Number, Title: row.Title, Status: Status(row.Status)}
	if c, err := s.opts.Targets.Get(ctx, row.ConnectionID); err == nil {
		n.Database = c.Name
	}
	users, err := s.opts.Directory.Users(ctx, tx, []string{row.AuthorID})
	if err != nil {
		return err
	}
	n.Author = users[row.AuthorID].Email
	if action == audit.ChangeApproved || action == audit.ChangeRejected {
		if a, ok := audit.ActorFrom(ctx); ok {
			n.Reviewer = a.Label()
		}
	}
	if base := settings.Get(s.opts.Settings, settings.InstancePublicURL); base != "" {
		n.URL = fmt.Sprintf("%s/changes/%d", strings.TrimRight(base, "/"), row.Number)
	}
	return s.opts.Notifier.Notify(ctx, tx, n)
}
