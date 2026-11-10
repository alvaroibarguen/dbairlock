// Package mcpserver is the MCP transport (PLAN.md 4.1 and 4.2): agents
// discover connections, read schemas and submit changes for human review at
// /mcp over streamable HTTP, authenticated by a personal access token.
//
// It is an adapter only. Every rule lives in the domain modules: changes
// strips row images for agent viewers, refuses agent reviews, deduplicates
// retried submissions and shows an agent only its token owner's changes;
// targets reads schemas from its cache. This package resolves connection
// names, shapes results for a model, maps errors to actionable text, rate
// limits per token and records one audit event per tool call.
//
// Output rule: no row images, cell values or other free text from a target
// database ever leave this package. Results carry identifiers and counts.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// MaxBodyBytes is the request body limit of /mcp (phase 8). Fixed: one
// tool call carries at most one change, 256 KB is far more than a change
// a person can review, and agents are the clients most likely to send
// something unbounded.
const MaxBodyBytes int64 = 256 << 10

// Changes is the changes module as the MCP server uses it.
type Changes interface {
	Preview(ctx context.Context, p identity.Principal, connectionID string, statements []string) (changes.PreviewResult, error)
	Create(ctx context.Context, p identity.Principal, in changes.Draft, submit bool) (changes.Change, error)
	Get(ctx context.Context, p identity.Principal, ref string) (changes.Change, error)
	List(ctx context.Context, p identity.Principal, f changes.ListFilter) (changes.Page, error)
	Cancel(ctx context.Context, p identity.Principal, ref, reason string) (changes.Change, error)
	CreateSequence(ctx context.Context, p identity.Principal, in changes.SequenceDraft) (changes.Sequence, error)
	GetSequence(ctx context.Context, p identity.Principal, ref string) (changes.Sequence, error)
	CancelSequence(ctx context.Context, p identity.Principal, ref, reason string) (changes.Sequence, error)
}

// Targets is the targets module as the MCP server uses it.
type Targets interface {
	List(ctx context.Context) ([]targets.Connection, error)
	Schema(ctx context.Context, id string) (connector.Schema, error)
}

// AuditFunc records one audit event in its own store transaction. The actor
// comes from the context.
type AuditFunc func(ctx context.Context, ev audit.Event) error

// Options configure the MCP server.
type Options struct {
	Version  string
	Changes  Changes
	Targets  Targets
	Settings settings.Source
	Audit    AuditFunc
	Clock    clock.Clock
	Logger   *slog.Logger
}

// Server is the MCP server.
type Server struct {
	opts   Options
	mcp    *mcp.Server
	limits *limits
}

// Instructions is the server-level guidance sent to clients at
// initialization.
const Instructions = `dbairlock lets you propose row-level data changes (INSERT, UPDATE, DELETE) to production databases. A person reviews and approves every change before dbairlock runs it; you cannot approve or run anything yourself.
Workflow: call list_connections and find_tables to locate the right connection and table, then list_tables or get_schema for columns. Check your SQL with preview_change, then call submit_change and give the user the review URL it returns. Use get_change to follow up later; do not poll in a tight loop.
When one change must run only after another has run, possibly on different databases, use submit_sequence: its steps are reviewed and run in order, each atomic on its own database, and steps already run are not undone if a later one stops. Follow it with get_sequence.
Only single-table INSERT, UPDATE and DELETE statements are accepted, and UPDATE and DELETE need a WHERE clause. You see row counts, never row data.`

// New returns the MCP server.
func New(opts Options) (*Server, error) {
	if opts.Changes == nil || opts.Targets == nil || opts.Settings == nil || opts.Audit == nil ||
		opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("mcpserver: changes, targets, settings, audit, clock and logger are required")
	}
	s := &Server{opts: opts, limits: newLimits(opts.Clock, opts.Settings)}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "dbairlock", Title: "dbairlock", Version: opts.Version},
		&mcp.ServerOptions{
			Instructions: Instructions,
			Logger:       opts.Logger,
			// Tools only: no logging, prompts or resources.
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		})
	s.registerDiscovery()
	s.registerChanges()
	s.registerSequences()
	return s, nil
}

// Handler serves the streamable HTTP transport. It is stateless: every
// request stands alone, so no session lives on the server. Mount it behind
// identity.RequireBearer, which puts the token owner's principal in the
// request context.
func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		Logger:                       s.opts.Logger,
		MaxRequestBodyBytes:          MaxBodyBytes,
		PropagateRequestCancellation: true,
	})
}

// call is what every tool handler gets: the caller and the arguments.
type call[In any] struct {
	p  identity.Principal
	in In
}

// result is a tool's typed output and the one-line summary that goes first
// in the text content.
type result[Out any] struct {
	out     Out
	summary string
}

// tool describes one tool for addTool.
type tool[In, Out any] struct {
	def *mcp.Tool
	// kind selects the extra per-tool rate limit, if any.
	kind limitKind
	// args summarizes the arguments for the audit event. Never the SQL.
	args func(In) map[string]any
	run  func(ctx context.Context, c call[In]) (result[Out], error)
}

// addTool registers t, wrapped with authentication, rate limits, the audit
// event and error mapping.
func addTool[In, Out any](s *Server, t tool[In, Out]) {
	name := t.def.Name
	mcp.AddTool(s.mcp, t.def, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		p, ok := identity.PrincipalFrom(ctx)
		if !ok || p.UserID == "" {
			return nil, zero, agentErr(`Not authenticated: send an API token as "Authorization: Bearer dbal_pat_...".`)
		}
		if err := s.limits.allow(p.APITokenID, name, t.kind); err != nil {
			s.opts.Logger.Warn("mcp rate limit", slog.String("tool", name), slog.String("token", p.APITokenID))
			return nil, zero, err
		}
		payload := map[string]any{"tool": name}
		if p.Agent != "" {
			payload["agent"] = p.Agent
		}
		if t.args != nil {
			for k, v := range t.args(in) {
				payload[k] = v
			}
		}
		// The subject is the credential: an API token, or the OAuth grant
		// (connected app) whose access token made the call.
		subject := "api_token"
		if p.OAuth {
			subject = "oauth_grant"
		}
		// Recorded before the tool runs, so a call that cannot be audited
		// does not run (a preview is a real dry run on the target).
		if err := s.opts.Audit(ctx, audit.Event{
			Action: audit.MCPToolCalled, SubjectType: subject, SubjectID: p.APITokenID, Payload: payload,
		}); err != nil {
			s.opts.Logger.Error("mcp audit failed", slog.String("tool", name), slog.Any("error", err))
			return nil, zero, errInternal
		}
		res, err := t.run(ctx, call[In]{p: p, in: in})
		if err != nil {
			return nil, zero, s.toolError(ctx, name, err)
		}
		raw, err := json.Marshal(res.out)
		if err != nil {
			return nil, zero, s.toolError(ctx, name, err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: res.summary},
			&mcp.TextContent{Text: string(raw)},
		}}, res.out, nil
	})
}

// reviewURL is the dashboard page of change number, built from
// instance.public_url.
func (s *Server) reviewURL(number int64) string {
	base := strings.TrimRight(settings.Get(s.opts.Settings, settings.InstancePublicURL), "/")
	return base + "/changes/" + strconv.FormatInt(number, 10)
}

// connection resolves a connection by name among the active connections:
// exactly first, then ignoring case.
func (s *Server) connection(ctx context.Context, name string) (targets.Connection, error) {
	list, err := s.opts.Targets.List(ctx)
	if err != nil {
		return targets.Connection{}, err
	}
	name = strings.TrimSpace(name)
	for _, c := range list {
		if c.Name == name {
			return c, nil
		}
	}
	for _, c := range list {
		if strings.EqualFold(c.Name, name) {
			return c, nil
		}
	}
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = strconv.Quote(c.Name)
	}
	if len(names) == 0 {
		return targets.Connection{}, userErrorf("There is no connection named %q, and no connections are configured yet. An admin adds them in the dashboard.", name)
	}
	return targets.Connection{}, userErrorf("There is no connection named %q. The connections are: %s. Call list_connections for details.",
		name, strings.Join(names, ", "))
}
