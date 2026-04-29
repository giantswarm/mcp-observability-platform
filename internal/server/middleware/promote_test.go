package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func TestPromoteOAuthCaller_LiftsUserInfo(t *testing.T) {
	// The HTTP→MCP bridge must copy mcp-oauth's UserInfo onto the MCP-level
	// context so tool handlers can read the identity. Without promotion
	// handlers see nothing and the resolver errors with ErrNoCallerIdentity.
	ui := &providers.UserInfo{ID: "sub-1", Email: "alice@example.com", TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(oauth.ContextWithUserInfo(req.Context(), ui))

	promoted := PromoteOAuthCaller(context.Background(), req)
	c := authz.CallerFromContext(promoted)
	if c.Subject != "sub-1" {
		t.Errorf("Subject = %q, want sub-1", c.Subject)
	}
	if c.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", c.Email)
	}
	if c.TokenSource != "oauth" {
		t.Errorf("TokenSource = %q, want oauth", c.TokenSource)
	}
}

func TestPromoteOAuthCaller_NoIdentityPassesThrough(t *testing.T) {
	// A request with no mcp-oauth UserInfo on its context must not cause
	// PromoteOAuthCaller to panic or attach a zero-valued caller.
	req := httptest.NewRequest("GET", "/mcp", nil)
	promoted := PromoteOAuthCaller(context.Background(), req)
	if authz.CallerFromContext(promoted).Authenticated() {
		t.Errorf("PromoteOAuthCaller with no UserInfo must not attach a caller")
	}
}
