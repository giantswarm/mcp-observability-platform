package middleware

import (
	"context"
	"net/http"

	"github.com/giantswarm/mcp-oauth/handler"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// ExtractCaller pulls the caller identity attached to the request by
// mcp-oauth's ValidateToken middleware. Returns a zero Caller when no
// identity is present — downstream gates (RequireCaller / authz) treat
// that as unauthenticated.
func ExtractCaller(r *http.Request) authz.Caller {
	ui, ok := handler.UserInfoFromContext(r.Context())
	if !ok || ui == nil || ui.Issuer == "" || ui.ID == "" || (ui.ActorIssuer == "") != (ui.ActorSubject == "") {
		return authz.Caller{}
	}
	var chain []string
	for _, actor := range ui.ActorChain {
		chain = append(chain, actor.Subject)
	}
	caller := authz.Caller{
		Issuer:       ui.Issuer,
		Subject:      ui.ID,
		TokenSource:  string(ui.TokenSource),
		ActorSubject: ui.ActorSubject,
		ActorIssuer:  ui.ActorIssuer,
		ActorChain:   chain,
	}
	if ui.EmailVerified {
		caller.Email = ui.Email
	}
	return caller
}

// InjectCallerFromRequest extracts the caller identity from r and attaches
// it to ctx. Wired as mcpsrv.WithHTTPContextFunc / WithSSEContextFunc — it
// bridges the HTTP-level OAuth context (where mcp-oauth stores UserInfo)
// onto the MCP-level handler context tool handlers see.
func InjectCallerFromRequest(ctx context.Context, r *http.Request) context.Context {
	return authz.WithCaller(ctx, ExtractCaller(r))
}
