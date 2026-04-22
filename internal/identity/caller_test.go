package identity

import (
	"context"
	"testing"

	"github.com/giantswarm/mcp-oauth/providers"
)

func TestCallerFromContext(t *testing.T) {
	// Empty context returns (nil, false).
	if ui, ok := CallerFromContext(context.Background()); ok || ui != nil {
		t.Errorf("empty ctx = (%v, %v), want (nil, false)", ui, ok)
	}

	want := &providers.UserInfo{ID: "user-1", Email: "u@example.com"}
	ctx := WithCaller(context.Background(), want)
	got, ok := CallerFromContext(ctx)
	if !ok {
		t.Fatalf("CallerFromContext after WithCaller: ok = false")
	}
	if got != want {
		t.Errorf("CallerFromContext returned %v, want %v", got, want)
	}
}

func TestWithCaller_NilIsANoOp(t *testing.T) {
	// Nil UserInfo must not attach an empty value — downstream callers check
	// `ok` on CallerFromContext and treat missing identity as "no access".
	ctx := WithCaller(context.Background(), nil)
	if _, ok := CallerFromContext(ctx); ok {
		t.Errorf("WithCaller(nil) should not attach identity")
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
				ctx = WithCaller(ctx, c.ui)
			}
			if got := CallerSubject(ctx); got != c.want {
				t.Errorf("CallerSubject = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallerAuthz_LeavesLoginEmpty(t *testing.T) {
	// Critical invariant: Login is NEVER populated from UserInfo.ID. OIDC sub
	// is not a Grafana login name; collapsing the two would make Grafana's
	// /api/users/lookup silently miss when the caller's email/login doesn't
	// match their sub. The resolver falls back to Email-based lookup when
	// Login is empty, which is the correct behaviour.
	ctx := WithCaller(context.Background(), &providers.UserInfo{
		ID:    "sub-1",
		Email: "alice@example.com",
	})
	got := CallerAuthz(ctx)
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got.Email)
	}
	if got.Subject != "sub-1" {
		t.Errorf("Subject = %q, want sub-1", got.Subject)
	}
}

func TestCallerAuthz_EmptyOnMissingIdentity(t *testing.T) {
	got := CallerAuthz(context.Background())
	if got.Email != "" || got.Subject != "" {
		t.Errorf("CallerAuthz without identity = %+v, want empty Caller", got)
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
		{"unset TokenSource is empty string", &providers.UserInfo{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.ui != nil {
				ctx = WithCaller(ctx, c.ui)
			}
			if got := CallerTokenSource(ctx); got != c.want {
				t.Errorf("CallerTokenSource = %q, want %q", got, c.want)
			}
		})
	}
}
