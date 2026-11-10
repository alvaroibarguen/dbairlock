package httpapi

import (
	"context"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// userBody renders a user. It is the one place outside identity that reads
// a role, to display it; authorization never does (identity.Can).
func userBody(u identity.User) gen.User {
	out := gen.User{
		Id: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt,
		Role:         gen.Role(u.Role), //nolint:forbidigo // displaying the role, not authorizing
		LastSignInAt: u.LastSignInAt, RemovedAt: u.RemovedAt,
	}
	if u.PictureURL != "" {
		pic := u.PictureURL
		out.PictureUrl = &pic
	}
	if u.RemovedAt != nil {
		by := u.RemovedBy
		out.RemovedBy = &by
	}
	return out
}

// ListUsers returns every user. The identity module authorizes.
func (s *Server) ListUsers(ctx context.Context, _ gen.ListUsersRequestObject) (gen.ListUsersResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.deps.Identity.ListUsers(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]gen.User, len(users))
	for i, u := range users {
		out[i] = userBody(u)
	}
	return gen.ListUsers200JSONResponse{Users: out}, nil
}

// UpdateUser changes a user's role.
func (s *Server) UpdateUser(ctx context.Context, req gen.UpdateUserRequestObject) (gen.UpdateUserResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.deps.Identity.SetRole(ctx, p, req.Id, identity.Role(req.Body.Role))
	if err != nil {
		return nil, err
	}
	return gen.UpdateUser200JSONResponse(userBody(u)), nil
}

// RemoveUser offboards a user.
func (s *Server) RemoveUser(ctx context.Context, req gen.RemoveUserRequestObject) (gen.RemoveUserResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.deps.Identity.RemoveUser(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.RemoveUser200JSONResponse(userBody(u)), nil
}

// RestoreUser restores a removed user.
func (s *Server) RestoreUser(ctx context.Context, req gen.RestoreUserRequestObject) (gen.RestoreUserResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.deps.Identity.RestoreUser(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.RestoreUser200JSONResponse(userBody(u)), nil
}

func tokenBody(t identity.Token) gen.ApiToken {
	return gen.ApiToken{
		Id: t.ID, Name: t.Name, Prefix: t.Prefix, OwnerId: t.UserID, OwnerEmail: t.OwnerEmail,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
	}
}

// ListTokens lists API tokens.
func (s *Server) ListTokens(ctx context.Context, req gen.ListTokensRequestObject) (gen.ListTokensResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	all := req.Params.All != nil && *req.Params.All
	tokens, err := s.deps.Identity.ListTokens(ctx, p, all)
	if err != nil {
		return nil, err
	}
	out := make([]gen.ApiToken, len(tokens))
	for i, t := range tokens {
		out[i] = tokenBody(t)
	}
	return gen.ListTokens200JSONResponse{Tokens: out}, nil
}

// CreateToken creates an API token and returns it once.
func (s *Server) CreateToken(ctx context.Context, req gen.CreateTokenRequestObject) (gen.CreateTokenResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	days := 0
	if req.Body.ExpiresInDays != nil {
		days = int(*req.Body.ExpiresInDays)
	}
	t, err := s.deps.Identity.CreateToken(ctx, p, req.Body.Name, days)
	if err != nil {
		return nil, err
	}
	base := ""
	if s.deps.Settings != nil {
		base = settings.Get(s.deps.Settings, settings.InstancePublicURL)
	}
	if base == "" {
		base = requestOrigin(pairFrom(ctx).r)
	}
	return gen.CreateToken201JSONResponse{
		Token: tokenBody(t.Token), Secret: t.Secret, McpUrl: strings.TrimRight(base, "/") + "/mcp",
	}, nil
}

// RevokeToken revokes an API token.
func (s *Server) RevokeToken(ctx context.Context, req gen.RevokeTokenRequestObject) (gen.RevokeTokenResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Identity.RevokeToken(ctx, p, req.Id); err != nil {
		return nil, err
	}
	return gen.RevokeToken204Response{}, nil
}
