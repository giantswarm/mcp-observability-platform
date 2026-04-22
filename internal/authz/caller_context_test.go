package authz

import (
	"context"
	"net/http/httptest"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"
)

func TestWithCaller_NilIsNoOp(t *testing.T) {
	// Nil UserInfo must not attach an empty value; downstream accessors
	// treat missing identity as "no access" and would otherwise mistake
	// a zero-valued UserInfo for an authenticated caller.
	ctx := withCaller(context.Background(), nil)
	if _, ok := userInfoFromContext(ctx); ok {
		t.Errorf("withCaller(nil) should not attach identity")
	}
}

func TestCallerSubject_Fallback(t *testing.T) {
	cases := []struct {
		name string
		ui   *providers.UserInfo
		want string
	}{
		{"no identity", nil, ""},
		{"id preferred over email", &providers.UserInfo{ID: "sub-1", Email: "u@e.com"}, "sub-1"},
		{"email fallback when id empty", &providers.UserInfo{Email: "u@e.com"}, "u@e.com"},
		{"both empty", &providers.UserInfo{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.ui != nil {
				ctx = withCaller(ctx, c.ui)
			}
			if got := CallerSubject(ctx); got != c.want {
				t.Errorf("CallerSubject = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallerTokenSource(t *testing.T) {
	cases := []struct {
		name string
		ui   *providers.UserInfo
		want string
	}{
		{"no identity", nil, ""},
		{"oauth flow", &providers.UserInfo{TokenSource: providers.TokenSourceOAuth}, "oauth"},
		{"sso forwarded", &providers.UserInfo{TokenSource: providers.TokenSourceSSO}, "sso"},
		{"unset TokenSource is empty", &providers.UserInfo{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.ui != nil {
				ctx = withCaller(ctx, c.ui)
			}
			if got := CallerTokenSource(ctx); got != c.want {
				t.Errorf("CallerTokenSource = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallerFromContext_LeavesLoginEmpty(t *testing.T) {
	// Critical invariant: there is no Login field to populate from
	// UserInfo.ID. OIDC sub is NOT a Grafana login name; collapsing the
	// two would make Grafana's /api/users/lookup silently miss when the
	// caller's email/login doesn't match their sub. The resolver falls
	// back to Email-based lookup when only Subject is set.
	ctx := withCaller(context.Background(), &providers.UserInfo{
		ID:    "sub-1",
		Email: "alice@example.com",
	})
	got := CallerFromContext(ctx)
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got.Email)
	}
	if got.Subject != "sub-1" {
		t.Errorf("Subject = %q, want sub-1", got.Subject)
	}
}

func TestCallerFromContext_EmptyOnMissingIdentity(t *testing.T) {
	got := CallerFromContext(context.Background())
	if got.Email != "" || got.Subject != "" {
		t.Errorf("CallerFromContext without identity = %+v, want empty", got)
	}
}

func TestPromoteOAuthCaller_LiftsUserInfo(t *testing.T) {
	// The HTTP→MCP bridge must copy mcp-oauth's UserInfo onto the MCP-level
	// context so tool handlers can read the identity. Without promotion
	// handlers see nothing and the resolver errors with ErrNoCallerIdentity.
	want := &providers.UserInfo{ID: "sub-1", Email: "alice@example.com"}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(oauth.ContextWithUserInfo(req.Context(), want))

	promoted := PromoteOAuthCaller(context.Background(), req)
	if got := CallerSubject(promoted); got != "sub-1" {
		t.Errorf("CallerSubject after promotion = %q, want sub-1", got)
	}
}

func TestPromoteOAuthCaller_NoIdentityPassesThrough(t *testing.T) {
	// A request with no mcp-oauth UserInfo on its context must not cause
	// PromoteOAuthCaller to panic or attach a zero-valued caller.
	req := httptest.NewRequest("GET", "/mcp", nil)
	promoted := PromoteOAuthCaller(context.Background(), req)
	if got := CallerSubject(promoted); got != "" {
		t.Errorf("CallerSubject with no identity = %q, want empty", got)
	}
}
