package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oapi-codegen/nullable"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// Settings is the settings module as the REST API uses it. Secrets go
// through it as write-only values; nothing here can read one back.
type Settings interface {
	settings.Source
	Values() map[string]any
	SecretStatus(ctx context.Context) (map[string]secrets.Meta, error)
	Update(ctx context.Context, ch settings.Changes) error
}

// GetSettings returns every setting and the status of related secrets.
func (s *Server) GetSettings(ctx context.Context, _ gen.GetSettingsRequestObject) (gen.GetSettingsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	body, err := s.settingsBody(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetSettings200JSONResponse(body), nil
}

// UpdateSettings applies a settings update atomically.
func (s *Server) UpdateSettings(ctx context.Context, req gen.UpdateSettingsRequestObject) (gen.UpdateSettingsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	ch, err := settingsChanges(req.Body)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Settings.Update(ctx, ch); err != nil {
		return nil, err // a *settings.ValidationError becomes 422 in mapError
	}
	body, err := s.settingsBody(ctx)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSettings200JSONResponse(body), nil
}

func (s *Server) settingsBody(ctx context.Context) (gen.Settings, error) {
	var body gen.Settings
	// The registry and the contract use the same keys (a contract test keeps
	// them in step), so a JSON round trip maps one onto the other.
	raw, err := json.Marshal(s.deps.Settings.Values())
	if err != nil {
		return body, fmt.Errorf("httpapi: encode settings: %w", err)
	}
	if err := json.Unmarshal(raw, &body.Values); err != nil {
		return body, fmt.Errorf("httpapi: decode settings: %w", err)
	}
	status, err := s.deps.Settings.SecretStatus(ctx)
	if err != nil {
		return body, err
	}
	body.Secrets = gen.SettingsSecretStatus{
		AuthGoogleClientSecret: secretStatus(status["auth.google.client_secret"]),
		NotifyWebhookUrl:       secretStatus(status["notify.webhook_url"]),
	}
	return body, nil
}

// secretStatus converts secret metadata to the wire shape. It is the only
// way a secret appears in a response.
func secretStatus(m secrets.Meta) gen.SecretStatus {
	if !m.IsSet {
		return gen.SecretStatus{IsSet: false}
	}
	by, at := m.SetBy, m.SetAt
	return gen.SecretStatus{IsSet: true, SetBy: &by, SetAt: &at}
}

func settingsChanges(body *gen.SettingsUpdate) (settings.Changes, error) {
	ch := settings.Changes{Values: map[string]any{}, Secrets: map[string]settings.SecretChange{}}
	if body == nil {
		return ch, nil
	}
	if body.Values != nil {
		raw, err := json.Marshal(body.Values)
		if err != nil {
			return ch, fmt.Errorf("httpapi: encode settings: %w", err)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return ch, fmt.Errorf("httpapi: decode settings: %w", err)
		}
		for k, v := range values {
			ch.Values[k] = v
		}
	}
	if body.Secrets != nil {
		addSecretChange(ch.Secrets, "auth.google.client_secret", body.Secrets.AuthGoogleClientSecret)
		addSecretChange(ch.Secrets, "notify.webhook_url", body.Secrets.NotifyWebhookUrl)
	}
	return ch, nil
}

// addSecretChange applies the update semantics for a secret: omitted means
// unchanged, null means clear, a string means replace.
func addSecretChange(out map[string]settings.SecretChange, name string, f nullable.Nullable[string]) {
	switch {
	case !f.IsSpecified():
	case f.IsNull():
		out[name] = settings.SecretChange{Clear: true}
	default:
		out[name] = settings.SecretChange{Value: secrets.NewValue(f.MustGet())}
	}
}
