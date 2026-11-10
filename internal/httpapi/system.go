package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/notify"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// SystemInfo is what the System page shows. It holds no secrets.
type SystemInfo struct {
	Version          string
	Commit           string
	DataDir          string
	StoreBytes       int64
	Dev              bool
	Users            int64
	Connections      int64
	Changes          int64
	MigrationCurrent int64
	MigrationLatest  int64
}

// System reports the state of the instance; app assembles it.
type System interface {
	Info(ctx context.Context) (SystemInfo, error)
}

// Notifications is the notify module as the REST API uses it.
type Notifications interface {
	Recent(ctx context.Context, limit int) ([]notify.Delivery, error)
	SendTest(ctx context.Context) (notify.TestResult, error)
}

// recentNotifications is how many notifications GET /api/notifications lists.
const recentNotifications = 25

var errNoWebhook = httpx.NewError(http.StatusConflict, "no_webhook",
	"No webhook URL is set. Save one first.")

// GetSystem returns the System page's data.
func (s *Server) GetSystem(ctx context.Context, _ gen.GetSystemRequestObject) (gen.GetSystemResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	info, err := s.deps.System.Info(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.GetSystem200JSONResponse{
		Version: info.Version, Commit: info.Commit, DataDir: info.DataDir, StoreBytes: info.StoreBytes,
		Dev:    info.Dev,
		Counts: gen.SystemCounts{Users: info.Users, Connections: info.Connections, Changes: info.Changes},
	}
	out.Migration.Current = info.MigrationCurrent
	out.Migration.Latest = info.MigrationLatest
	out.Migration.Pending = info.MigrationCurrent < info.MigrationLatest
	last, ok, err := s.deps.AuditCheck.LastSuccess(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		out.LastVerification = &gen.AuditLastSuccess{At: last.At, By: last.By, Checked: last.Checked}
	}
	return out, nil
}

// ListNotifications lists recent notifications and their outcome.
func (s *Server) ListNotifications(ctx context.Context, _ gen.ListNotificationsRequestObject) (gen.ListNotificationsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	list, err := s.deps.Notify.Recent(ctx, recentNotifications)
	if err != nil {
		return nil, err
	}
	out := gen.ListNotifications200JSONResponse{Notifications: make([]gen.Notification, 0, len(list))}
	for _, d := range list {
		n := gen.Notification{
			Id: d.ID, Event: d.Event, Status: gen.NotificationStatus(d.Status), Attempts: d.Attempts,
			CreatedAt: d.CreatedAt, LastError: optStr(d.LastError),
		}
		if d.Status == "pending" {
			n.NextAttemptAt = ptr(d.NextAttemptAt)
		}
		if !d.FinishedAt.IsZero() {
			n.FinishedAt = ptr(d.FinishedAt)
		}
		if d.LastStatus != 0 {
			n.LastStatus = ptr(d.LastStatus)
		}
		out.Notifications = append(out.Notifications, n)
	}
	return out, nil
}

// SendTestNotification posts a test event to the saved webhook now.
func (s *Server) SendTestNotification(ctx context.Context, _ gen.SendTestNotificationRequestObject) (gen.SendTestNotificationResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	res, err := s.deps.Notify.SendTest(ctx)
	if errors.Is(err, notify.ErrNoWebhook) {
		return nil, errNoWebhook
	}
	if err != nil {
		return nil, err
	}
	out := gen.SendTestNotification200JSONResponse{Ok: res.OK, Message: res.Message}
	if res.StatusCode != 0 {
		out.StatusCode = ptr(res.StatusCode)
	}
	return out, nil
}
