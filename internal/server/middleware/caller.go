package middleware

import (
	"context"
	"net/http"

	oauth "github.com/giantswarm/mcp-oauth"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// ExtractCaller pulls the caller identity attached to the request by
// mcp-oauth's ValidateToken middleware. Returns a zero Caller when no
// identity is present — downstream gates (RequireCaller / authz) treat
// that as unauthenticated.
func ExtractCaller(r *http.Request) authz.Caller {
	ui, ok := oauth.UserInfoFromContext(r.Context())
	if !ok || ui == nil {
		return authz.Caller{}
	}
	return authz.Caller{
		Subject:     ui.ID,
		Email:       ui.Email,
		TokenSource: string(ui.TokenSource),
	}
}

// InjectCallerFromRequest extracts the caller identity from r and attaches
// it to ctx. Wired as mcpsrv.WithHTTPContextFunc / WithSSEContextFunc — it
// bridges the HTTP-level OAuth context (where mcp-oauth stores UserInfo)
// onto the MCP-level handler context tool handlers see.
func InjectCallerFromRequest(ctx context.Context, r *http.Request) context.Context {
	return authz.WithCaller(ctx, ExtractCaller(r))
}
