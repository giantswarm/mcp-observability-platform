package middleware

import (
	"context"
	"net/http"

	"github.com/giantswarm/mcp-oauth/handler"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// ExtractCaller pulls the caller identity attached to the request by
// mcp-oauth's ValidateToken middleware. Returns a zero Caller when no
// identity is present — downstream gates (RequireCaller / authz) treat
// that as unauthenticated.
func ExtractCaller(r *http.Request) authz.Caller {
	ui, ok := handler.UserInfoFromContext(r.Context())
	if !ok || ui == nil {
		return authz.Caller{}
	}
	var chain []string
	for _, actor := range ui.ActorChain {
		chain = append(chain, actor.Subject)
	}
	return authz.Caller{
		Subject:      ui.ID,
		Email:        ui.Email,
		TokenSource:  string(ui.TokenSource),
		ActorSubject: ui.ActorSubject,
		ActorChain:   chain,
	}
}

// InjectCallerFromRequest extracts the caller identity from r and attaches
// it to ctx. Wired as mcpsrv.WithHTTPContextFunc / WithSSEContextFunc — it
// bridges the HTTP-level OAuth context (where mcp-oauth stores UserInfo)
// onto the MCP-level handler context tool handlers see.
func InjectCallerFromRequest(ctx context.Context, r *http.Request) context.Context {
	return authz.WithCaller(ctx, ExtractCaller(r))
}

// TokenResolver returns the caller's own Dex ID token for r, or "" when
// the caller has none the MCP can forward.
type TokenResolver func(ctx context.Context, r *http.Request) string

// InjectCaller is InjectCallerFromRequest plus, when resolve is non-nil,
// the caller's Dex ID token attached via grafana.WithUserToken (JWT auth
// mode). resolve == nil behaves exactly like InjectCallerFromRequest.
func InjectCaller(resolve TokenResolver) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		ctx = InjectCallerFromRequest(ctx, r)
		if resolve == nil {
			return ctx
		}
		if tok := resolve(ctx, r); tok != "" {
			ctx = grafana.WithUserToken(ctx, tok)
		}
		return ctx
	}
}
