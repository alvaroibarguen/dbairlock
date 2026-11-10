package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Targets is the targets module as the REST API uses it. Passwords go in as
// write-only values; nothing here can read one back.
type Targets interface {
	Connectors() []connector.Connector
	List(ctx context.Context) ([]targets.Connection, error)
	Get(ctx context.Context, id string) (targets.Connection, error)
	Create(ctx context.Context, in targets.CreateInput) (targets.Connection, error)
	Update(ctx context.Context, id string, in targets.UpdateInput) (targets.Connection, error)
	Archive(ctx context.Context, id string) (targets.Connection, error)
	TestUnsaved(ctx context.Context, in targets.TestInput) (targets.TestResult, error)
	Test(ctx context.Context, id string) (targets.TestResult, error)
	Schema(ctx context.Context, id string) (connector.Schema, error)
}

// connectionResource is what connection actions apply to. Reading
// connections (list, details, schema) is authorized as change.submit: every
// person who may submit a change needs to see where it can go.
func connectionResource(id string) identity.Resource {
	return identity.Resource{Type: "connection", ID: id}
}

var errTargetUnavailable = func(msg string) *httpx.Error {
	return httpx.NewError(http.StatusBadGateway, "target_unavailable", msg)
}

// ListConnectors returns every connector and its connection form.
func (s *Server) ListConnectors(ctx context.Context, _ gen.ListConnectorsRequestObject) (gen.ListConnectorsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionChangeSubmit, connectionResource("")); err != nil {
		return nil, err
	}
	out := []gen.Connector{}
	for _, c := range s.deps.Targets.Connectors() {
		info := c.Info()
		fields := []gen.ConnectorField{}
		for _, f := range c.ConfigFields() {
			gf := gen.ConnectorField{
				Key: f.Key, Label: f.Label, Type: gen.ConnectorFieldType(f.Type),
				Required: f.Required, Default: f.Default,
			}
			if len(f.Options) > 0 {
				opts := append([]string{}, f.Options...)
				gf.Options = &opts
			}
			if f.Help != "" {
				help := f.Help
				gf.Help = &help
			}
			fields = append(fields, gf)
		}
		out = append(out, gen.Connector{
			Id: info.ID, DisplayName: info.DisplayName, Dialect: info.Dialect,
			DefaultPort: info.DefaultPort, Fields: fields,
		})
	}
	return gen.ListConnectors200JSONResponse{Connectors: out, DefaultAllowedFunctions: connector.DefaultAllowedFunctions()}, nil
}

// ListConnections lists connections that are not archived.
func (s *Server) ListConnections(ctx context.Context, _ gen.ListConnectionsRequestObject) (gen.ListConnectionsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionChangeSubmit, connectionResource("")); err != nil {
		return nil, err
	}
	list, err := s.deps.Targets.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Connection, len(list))
	for i, c := range list {
		out[i] = connectionBody(c)
	}
	return gen.ListConnections200JSONResponse{Connections: out}, nil
}

// GetConnection returns one connection.
func (s *Server) GetConnection(ctx context.Context, req gen.GetConnectionRequestObject) (gen.GetConnectionResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionChangeSubmit, connectionResource(req.Id)); err != nil {
		return nil, err
	}
	c, err := s.deps.Targets.Get(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetConnection200JSONResponse(connectionBody(c)), nil
}

// CreateConnection adds a connection.
func (s *Server) CreateConnection(ctx context.Context, req gen.CreateConnectionRequestObject) (gen.CreateConnectionResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionDatabaseManage, connectionResource("")); err != nil {
		return nil, err
	}
	b := req.Body
	in := targets.CreateInput{
		Name: b.Name, Connector: b.Connector, Config: connector.Config(b.Config),
		Policy: policyPatch(b.Policy),
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Password != nil {
		in.Password = secrets.NewValue(*b.Password)
	}
	c, err := s.deps.Targets.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	return gen.CreateConnection201JSONResponse(connectionBody(c)), nil
}

// UpdateConnection changes a connection.
func (s *Server) UpdateConnection(ctx context.Context, req gen.UpdateConnectionRequestObject) (gen.UpdateConnectionResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionDatabaseManage, connectionResource(req.Id)); err != nil {
		return nil, err
	}
	b := req.Body
	in := targets.UpdateInput{Name: b.Name, Description: b.Description, Policy: policyPatch(b.Policy)}
	if b.Config != nil {
		in.Config = connector.Config(*b.Config)
		if in.Config == nil {
			in.Config = connector.Config{}
		}
	}
	if b.Password != nil {
		v := secrets.NewValue(*b.Password)
		in.Password = &v
	}
	c, err := s.deps.Targets.Update(ctx, req.Id, in)
	if err != nil {
		return nil, err
	}
	return gen.UpdateConnection200JSONResponse(connectionBody(c)), nil
}

// ArchiveConnection archives a connection.
func (s *Server) ArchiveConnection(ctx context.Context, req gen.ArchiveConnectionRequestObject) (gen.ArchiveConnectionResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionDatabaseManage, connectionResource(req.Id)); err != nil {
		return nil, err
	}
	c, err := s.deps.Targets.Archive(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.ArchiveConnection200JSONResponse(connectionBody(c)), nil
}

// TestConnectionSettings tests settings that are not saved.
func (s *Server) TestConnectionSettings(ctx context.Context, req gen.TestConnectionSettingsRequestObject) (gen.TestConnectionSettingsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionDatabaseManage, connectionResource("")); err != nil {
		return nil, err
	}
	b := req.Body
	in := targets.TestInput{Connector: b.Connector, Config: connector.Config(b.Config)}
	if b.Password != nil {
		v := secrets.NewValue(*b.Password)
		in.Password = &v
	}
	if b.ConnectionId != nil {
		in.ConnectionID = *b.ConnectionId
	}
	res, err := s.deps.Targets.TestUnsaved(ctx, in)
	if err != nil {
		return nil, err
	}
	return gen.TestConnectionSettings200JSONResponse(testBody(res)), nil
}

// TestConnection tests a saved connection and records the outcome.
func (s *Server) TestConnection(ctx context.Context, req gen.TestConnectionRequestObject) (gen.TestConnectionResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionDatabaseManage, connectionResource(req.Id)); err != nil {
		return nil, err
	}
	res, err := s.deps.Targets.Test(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.TestConnection200JSONResponse(testBody(res)), nil
}

// GetConnectionSchema returns the (cached) schema of a connection.
func (s *Server) GetConnectionSchema(ctx context.Context, req gen.GetConnectionSchemaRequestObject) (gen.GetConnectionSchemaResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionChangeSubmit, connectionResource(req.Id)); err != nil {
		return nil, err
	}
	sc, err := s.deps.Targets.Schema(ctx, req.Id)
	switch {
	case err == nil:
	case errors.Is(err, targets.ErrNotFound), errors.Is(err, targets.ErrArchived), errors.Is(err, targets.ErrNoPassword):
		return nil, err
	default:
		return nil, errTargetUnavailable("The schema could not be read: " + err.Error())
	}
	out := gen.Schema{Namespaces: []gen.SchemaNamespace{}}
	for _, ns := range sc.Namespaces {
		gns := gen.SchemaNamespace{Name: ns.Name, Tables: []gen.SchemaTable{}}
		for _, t := range ns.Tables {
			gt := gen.SchemaTable{Name: t.Name, Columns: []gen.SchemaColumn{}, PrimaryKey: append([]string{}, t.PrimaryKey...)}
			for _, c := range t.Columns {
				gt.Columns = append(gt.Columns, gen.SchemaColumn{Name: c.Name, Type: c.Type, Nullable: c.Nullable})
			}
			gns.Tables = append(gns.Tables, gt)
		}
		out.Namespaces = append(out.Namespaces, gns)
	}
	return gen.GetConnectionSchema200JSONResponse(out), nil
}

func policyPatch(p *gen.ConnectionPolicyPatch) targets.PolicyPatch {
	if p == nil {
		return targets.PolicyPatch{}
	}
	return targets.PolicyPatch{
		RequiredApprovals: p.RequiredApprovals, MaxRows: p.MaxRows, ApprovalTTLHours: p.ApprovalTtlHours,
		StoreRowImages: p.StoreRowImages, AutoExecute: p.AutoExecute, AllowedFunctions: p.AllowedFunctions,
	}
}

// connectionBody renders a connection. The password appears only as its
// status (secretStatus), never as a value.
func connectionBody(c targets.Connection) gen.Connection {
	cfg := gen.ConnectionConfig{}
	for k, v := range c.Config {
		cfg[k] = v
	}
	out := gen.Connection{
		Id: c.ID, Name: c.Name, Description: c.Description, Connector: c.Connector, Config: cfg,
		Policy: gen.ConnectionPolicy{
			RequiredApprovals: c.Policy.RequiredApprovals, MaxRows: c.Policy.MaxRows,
			ApprovalTtlHours: c.Policy.ApprovalTTLHours, StoreRowImages: c.Policy.StoreRowImages,
			AutoExecute: c.Policy.AutoExecute, AllowedFunctions: append([]string{}, c.Policy.AllowedFunctions...),
		},
		Password:  secretStatus(c.Password),
		CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		ArchivedAt: c.ArchivedAt,
	}
	if c.ArchivedAt != nil {
		by := c.ArchivedBy
		out.ArchivedBy = &by
	}
	if c.LastTest != nil {
		t := testBody(*c.LastTest)
		out.LastTest = &t
	}
	return out
}

func testBody(r targets.TestResult) gen.ConnectionTestResult {
	out := gen.ConnectionTestResult{Ok: r.OK, TestedAt: r.TestedAt, Warnings: append([]string{}, r.Warnings...)}
	if r.Message != "" {
		msg := r.Message
		out.Message = &msg
	}
	if r.Server != nil {
		out.Server = &gen.ServerInfo{
			Product: r.Server.Product, Version: r.Server.Version, Major: r.Server.Major, Supported: r.Server.Supported,
		}
	}
	if p := r.Privileges; p != nil {
		rep := gen.PrivilegeReport{
			User: p.User, CanSelect: p.CanSelect, CanInsert: p.CanInsert, CanUpdate: p.CanUpdate,
			CanDelete: p.CanDelete, Excess: []gen.ExcessPrivilege{},
		}
		for _, e := range p.Excess {
			ge := gen.ExcessPrivilege{Privilege: e.Privilege, Detail: e.Detail}
			if e.Fix != "" {
				fix := e.Fix
				ge.Fix = &fix
			}
			rep.Excess = append(rep.Excess, ge)
		}
		out.Privileges = &rep
	}
	return out
}
