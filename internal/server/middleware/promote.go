package middleware

import (
	"context"
	"net/http"

	oauth "github.com/giantswarm/mcp-oauth"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// PromoteOAuthCaller lifts the UserInfo attached by mcp-oauth's ValidateToken
// middleware onto the context mcp-go passes to tool handlers. Intended as
// the argument to mcpsrv.WithHTTPContextFunc / WithSSEContextFunc — it
// bridges HTTP-level OAuth state into MCP-level handler context. Callers
// that reach a tool without a valid identity are rejected by RequireCaller
// (and at the authz boundary via ErrNoCallerIdentity).
func PromoteOAuthCaller(ctx context.Context, r *http.Request) context.Context {
	ui, ok := oauth.UserInfoFromContext(r.Context())
	if !ok || ui == nil {
		return ctx
	}
	return authz.WithCaller(ctx, authz.Caller{
		Subject:     ui.ID,
		Email:       ui.Email,
		TokenSource: string(ui.TokenSource),
	})
}
