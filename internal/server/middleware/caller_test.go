package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-oauth/providers/oidc"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

const (
	testSubject    = "sub-1"
	testAliceEmail = "alice@example.com"
)

func TestExtractCaller_ReturnsAttachedIdentity(t *testing.T) {
	ui := &providers.UserInfo{ID: testSubject, Email: testAliceEmail, TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(handler.ContextWithUserInfo(req.Context(), ui))

	c := ExtractCaller(req)
	if c.Subject != testSubject {
		t.Errorf("Subject = %q, want %s", c.Subject, testSubject)
	}
	if c.Email != testAliceEmail {
		t.Errorf("Email = %q, want alice@example.com", c.Email)
	}
	if c.TokenSource != "oauth" {
		t.Errorf("TokenSource = %q, want oauth", c.TokenSource)
	}
}

func TestExtractCaller_DelegatedTokenCarriesActor(t *testing.T) {
	const (
		agentSub   = "system:serviceaccount:kagent:sre-agent"
		plannerSub = "system:serviceaccount:kagent:planner-agent"
	)
	ui := &providers.UserInfo{
		ID:           testAliceEmail,
		Email:        testAliceEmail,
		TokenSource:  providers.TokenSourceTrustedIssuer,
		ActorSubject: agentSub,
		ActorChain: []oidc.ActorClaim{
			{Subject: agentSub},
			{Subject: plannerSub},
		},
	}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(handler.ContextWithUserInfo(req.Context(), ui))

	c := ExtractCaller(req)
	if c.ActorSubject != agentSub {
		t.Errorf("ActorSubject = %q, want %s", c.ActorSubject, agentSub)
	}
	if len(c.ActorChain) != 2 || c.ActorChain[0] != agentSub || c.ActorChain[1] != plannerSub {
		t.Errorf("ActorChain = %v, want [%s %s]", c.ActorChain, agentSub, plannerSub)
	}
	if c.TokenSource != "trusted-issuer" {
		t.Errorf("TokenSource = %q, want trusted-issuer", c.TokenSource)
	}
}

func TestExtractCaller_DirectTokenHasNoActor(t *testing.T) {
	ui := &providers.UserInfo{ID: testSubject, Email: testAliceEmail, TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(handler.ContextWithUserInfo(req.Context(), ui))

	c := ExtractCaller(req)
	if c.ActorSubject != "" || len(c.ActorChain) != 0 {
		t.Errorf("direct token must carry no actor, got ActorSubject=%q ActorChain=%v", c.ActorSubject, c.ActorChain)
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
	ui := &providers.UserInfo{ID: testSubject, Email: testAliceEmail, TokenSource: providers.TokenSourceOAuth}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(handler.ContextWithUserInfo(req.Context(), ui))

	ctx := InjectCallerFromRequest(context.Background(), req)
	c := authz.CallerFromContext(ctx)
	if c.Subject != testSubject {
		t.Errorf("Subject = %q, want %s", c.Subject, testSubject)
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
