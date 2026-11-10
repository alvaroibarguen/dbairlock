package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Formats of the webhook body (setting notify.format).
const (
	FormatGeneric = "generic"
	FormatSlack   = "slack"
)

// Render encodes ev as the webhook body in format.
//
// generic: one flat JSON object with "event", "occurred_at", "text", "url"
// and every field under its key.
//
// slack: the body Slack incoming webhooks accept. Text written by people or
// agents (titles) goes in plain_text blocks, so it can never become a link,
// a mention or markup; only the dashboard link uses mrkdwn.
func Render(format string, ev Event) ([]byte, error) {
	switch format {
	case FormatSlack:
		return json.Marshal(slackBody(ev))
	case FormatGeneric, "":
		return json.Marshal(genericBody(ev))
	}
	return nil, fmt.Errorf("notify: unknown format %q", format)
}

func genericBody(ev Event) map[string]any {
	out := map[string]any{
		"event":       ev.Type,
		"occurred_at": ev.OccurredAt.UTC().Format(time.RFC3339),
		"text":        ev.Text,
	}
	if ev.URL != "" {
		out["url"] = ev.URL
	}
	for _, f := range ev.Fields {
		if _, taken := out[f.Key]; !taken {
			out[f.Key] = f.Value
		}
	}
	return out
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

// slackEscape escapes the three characters Slack's mrkdwn treats as control
// characters, so text cannot form links or mentions.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Slack limits: 3000 characters per text, 10 fields per section.
const (
	slackMaxText   = 3000
	slackMaxFields = 10
)

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func slackBody(ev Event) map[string]any {
	blocks := []slackBlock{{Type: "section", Text: &slackText{Type: "plain_text", Text: clip(ev.Text, slackMaxText)}}}
	var fields []slackText
	for _, f := range ev.Fields {
		if len(fields) == slackMaxFields {
			break
		}
		fields = append(fields, slackText{Type: "plain_text", Text: clip(fmt.Sprintf("%s: %v", f.Label, f.Value), 2000)})
	}
	if len(fields) > 0 {
		blocks = append(blocks, slackBlock{Type: "section", Fields: fields})
	}
	fallback := slackEscape(ev.Text)
	if ev.URL != "" {
		link := "<" + slackEscape(ev.URL) + "|Open in dbairlock>"
		blocks = append(blocks, slackBlock{Type: "context", Elements: []slackText{{Type: "mrkdwn", Text: link}}})
		fallback += " " + link
	}
	return map[string]any{"text": clip(fallback, slackMaxText), "blocks": blocks}
}
