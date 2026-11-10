package app

import (
	"context"
	"path/filepath"
	"runtime/debug"
	"strconv"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/notify"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// changeNotifier hands change notices to the notify module, which knows
// about events and destinations but not about changes.
type changeNotifier struct{ n *notify.Service }

var statusLabels = map[changes.Status]string{
	changes.StatusPendingReview: "pending review",
	changes.StatusApproved:      "approved",
	changes.StatusExecuted:      "executed",
	changes.StatusFailed:        "failed",
	changes.StatusRejected:      "rejected",
}

var noticeVerbs = map[string]string{
	"change.submitted": "submitted for review",
	"change.approved":  "approved",
	"change.rejected":  "rejected",
	"change.executed":  "executed",
	"change.failed":    "failed",
}

func (c changeNotifier) Notify(ctx context.Context, tx *store.Tx, n changes.Notice) error {
	if n.Sequence {
		verb := "stopped"
		if n.Event == "sequence.completed" {
			verb = "completed"
		}
		return c.n.Enqueue(ctx, tx, notify.Event{
			Type: n.Event, URL: n.URL,
			Text: "Sequence #" + strconv.FormatInt(n.Number, 10) + " " + verb + ": " + n.Title,
			Fields: []notify.Field{
				{Key: "sequence", Label: "Sequence", Value: n.Number},
				{Key: "title", Label: "Title", Value: n.Title},
				{Key: "author", Label: "Author", Value: n.Author},
				{Key: "outcome", Label: "Outcome", Value: n.Message},
			},
		})
	}
	status := statusLabels[n.Status]
	if status == "" {
		status = string(n.Status)
	}
	fields := []notify.Field{
		{Key: "change", Label: "Change", Value: n.Number},
		{Key: "title", Label: "Title", Value: n.Title},
		{Key: "database", Label: "Database", Value: n.Database},
		{Key: "author", Label: "Author", Value: n.Author},
	}
	if n.Reviewer != "" {
		fields = append(fields, notify.Field{Key: "reviewer", Label: "Reviewer", Value: n.Reviewer})
	}
	if n.Step != "" {
		fields = append(fields, notify.Field{Key: "sequence_step", Label: "Sequence", Value: n.Step})
	}
	fields = append(fields, notify.Field{Key: "status", Label: "Status", Value: status})
	verb := noticeVerbs[n.Event]
	if verb == "" {
		verb = n.Event
	}
	return c.n.Enqueue(ctx, tx, notify.Event{
		Type: n.Event, URL: n.URL, Fields: fields,
		Text: "Change #" + strconv.FormatInt(n.Number, 10) + " " + verb + ": " + n.Title,
	})
}

// systemInfo assembles the System page from the modules.
type systemInfo struct {
	version  string
	commit   string
	dataDir  string
	dev      bool
	store    *store.Store
	identity *identity.Service
	targets  *targets.Service
	changes  *changes.Service
}

var _ httpapi.System = systemInfo{}

func (s systemInfo) Info(ctx context.Context) (httpapi.SystemInfo, error) {
	commit := s.commit
	if commit == "" {
		commit = buildCommit()
	}
	out := httpapi.SystemInfo{Version: s.version, Commit: commit, Dev: s.dev}
	if abs, err := filepath.Abs(s.dataDir); err == nil {
		out.DataDir = abs
	} else {
		out.DataDir = s.dataDir
	}
	var err error
	if out.StoreBytes, err = s.store.Size(); err != nil {
		return out, err
	}
	if out.MigrationCurrent, out.MigrationLatest, err = s.store.MigrationVersions(ctx); err != nil {
		return out, err
	}
	p, ok := identity.PrincipalFrom(ctx)
	if !ok {
		return out, identity.ErrUnauthenticated
	}
	users, err := s.identity.ListUsers(ctx, p)
	if err != nil {
		return out, err
	}
	for _, u := range users {
		if u.RemovedAt == nil {
			out.Users++
		}
	}
	conns, err := s.targets.List(ctx)
	if err != nil {
		return out, err
	}
	out.Connections = int64(len(conns))
	if out.Changes, err = s.changes.Count(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// buildCommit is the VCS revision Go recorded in the binary, or "unknown".
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
