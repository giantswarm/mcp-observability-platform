package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func TestExtractCaller_ReturnsAttachedIdentity(t *testing.T) {
	ui := &providers.UserInfo{ID: "sub-1", Email: "alice@example.com", TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(oauth.ContextWithUserInfo(req.Context(), ui))

	c := ExtractCaller(req)
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

func TestExtractCaller_NoIdentityReturnsZero(t *testing.T) {
	req := httptest.NewRequest("GET", "/mcp", nil)
	if c := ExtractCaller(req); c.Authenticated() {
		t.Errorf("ExtractCaller with no UserInfo must return zero Caller, got %+v", c)
	}
}

func TestInjectCallerFromRequest_AttachesToContext(t *testing.T) {
	// The HTTP→MCP bridge must copy mcp-oauth's UserInfo onto the MCP-level
	// context so tool handlers can read the identity. Without it handlers
	// see nothing and the resolver errors with ErrNoCallerIdentity.
	ui := &providers.UserInfo{ID: "sub-1", Email: "alice@example.com", TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(oauth.ContextWithUserInfo(req.Context(), ui))

	ctx := InjectCallerFromRequest(context.Background(), req)
	c := authz.CallerFromContext(ctx)
	if c.Subject != "sub-1" {
		t.Errorf("Subject = %q, want sub-1", c.Subject)
	}
	if c.TokenSource != "oauth" {
		t.Errorf("TokenSource = %q, want oauth", c.TokenSource)
	}
}

func TestInjectCallerFromRequest_NoIdentityPassesThrough(t *testing.T) {
	// A request with no mcp-oauth UserInfo on its context must not panic or
	// attach a zero-valued caller.
	req := httptest.NewRequest("GET", "/mcp", nil)
	ctx := InjectCallerFromRequest(context.Background(), req)
	if authz.CallerFromContext(ctx).Authenticated() {
		t.Errorf("InjectCallerFromRequest with no UserInfo must not attach a caller")
	}
}
