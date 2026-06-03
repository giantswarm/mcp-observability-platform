package authz

import (
	"context"
	"testing"
)

const testTokenOAuth = "oauth"

func TestWithCaller_UnauthenticatedIsNoOp(t *testing.T) {
	// An unauthenticated Caller (no Subject) must not attach; downstream
	// accessors treat missing identity as "no access" and would otherwise
	// mistake a zero-valued Caller for an authenticated one.
	ctx := WithCaller(context.Background(), Caller{})
	if c := CallerFromContext(ctx); c.Authenticated() {
		t.Errorf("WithCaller(unauthenticated) should not attach identity, got %+v", c)
	}
	ctx = WithCaller(context.Background(), Caller{Email: testEmail})
	if c := CallerFromContext(ctx); c.Authenticated() {
		t.Errorf("WithCaller(email-only) should not attach identity, got %+v", c)
	}
}

func TestCallerSubject_Fallback(t *testing.T) {
	cases := []struct {
		name string
		c    Caller
		want string
	}{
		{"no identity", Caller{}, ""},
		{"subject preferred over email", Caller{Subject: testSubject, Email: testEmail}, testSubject},
		{"email fallback when subject empty", Caller{Email: testEmail}, ""}, // Authenticated() rejects subject-less, so attach is a no-op
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := WithCaller(context.Background(), c.c)
			if got := CallerSubject(ctx); got != c.want {
				t.Errorf("CallerSubject = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallerTokenSource(t *testing.T) {
	cases := []struct {
		name string
		c    Caller
		want string
	}{
		{"no identity", Caller{}, ""},
		{"oauth flow", Caller{Subject: testSubject, TokenSource: testTokenOAuth}, testTokenOAuth},
		{"sso forwarded", Caller{Subject: testSubject, TokenSource: "sso"}, "sso"},
		{"unset TokenSource is empty", Caller{Subject: testSubject}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := WithCaller(context.Background(), c.c)
			if got := CallerTokenSource(ctx); got != c.want {
				t.Errorf("CallerTokenSource = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallerFromContext_RoundTrip(t *testing.T) {
	want := Caller{Subject: testSubject, Email: "alice@example.com", TokenSource: testTokenOAuth}
	got := CallerFromContext(WithCaller(context.Background(), want))
	if got != want {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

func TestCallerFromContext_EmptyOnMissingIdentity(t *testing.T) {
	got := CallerFromContext(context.Background())
	if got.Authenticated() {
		t.Errorf("CallerFromContext without identity = %+v, want unauthenticated", got)
	}
}
