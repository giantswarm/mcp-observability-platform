package authz

import "context"

// Caller contains validated issuer and subject claims. Email is populated only
// when verified by the identity provider. Authorization uses the human identity;
// actor claims describe validated delegation.
type Caller struct {
	Issuer       string
	Subject      string
	Email        string
	TokenSource  string
	ActorSubject string
	ActorIssuer  string
	ActorChain   []string
}

// Identity returns the best handle to pass to /api/users/lookup — Grafana
// stores users by email for OAuth-provisioned accounts, so Email comes
// first; Subject is the last-resort fallback.
func (c Caller) Identity() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Subject
}

// Authenticated requires complete principal and actor identities.
func (c Caller) Authenticated() bool {
	return c.Issuer != "" && c.Subject != "" && (c.ActorIssuer == "") == (c.ActorSubject == "")
}

// OrgLister is the authorizer's port onto "the set of known Grafana
// organisations". Implementations today wrap controller-runtime's informer
// cache of GrafanaOrganization CRs; tests implement it directly in-memory.
// Domain types only — the adapter is responsible for translating CR shapes
// into Organization, so tests need no CR imports.
type OrgLister interface {
	List(ctx context.Context) ([]Organization, error)
}

// callerKey is an unexported context key for the caller's validated
// identity. Unexported so external packages cannot overwrite the value.
type callerKey struct{}

// WithCaller attaches a Caller to ctx. An empty Caller is a no-op so the
// downstream gate (RequireCaller / Caller.Empty()) sees "no identity"
// rather than a zero-valued attached caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	if !c.Authenticated() {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFromContext extracts the Caller attached by WithCaller. Returns an
// empty Caller (Subject == "") when no identity is attached; the
// authorizer surfaces that as ErrNoCallerIdentity.
func CallerFromContext(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// CallerSubject returns the caller's audit handle for the X-Grafana-User
// header — Email preferred over Subject, empty when no identity is attached.
// Grafana provisions OAuth users by email and the authz lookup keys on email
// (see Identity), so email is the handle that resolves to a real Grafana user
// in the audit log; for a muster-minted token the sub is opaque while the
// email claim carries the human. Falls back to Subject when no email is set.
func CallerSubject(ctx context.Context) string {
	c := CallerFromContext(ctx)
	if c.Email != "" {
		return c.Email
	}
	return c.Subject
}

// CallerTokenSource returns the OAuth flavour that produced the caller's
// identity ("oauth" / "sso" / ""). Recorded on audit entries so direct and
// SSO-forwarded sessions are distinguishable.
func CallerTokenSource(ctx context.Context) string {
	return CallerFromContext(ctx).TokenSource
}

// CallerActorSubject returns the acting agent's subject for a delegated
// (on-behalf-of) caller, empty for direct sessions. Recorded on audit
// entries so delegated calls name the agent alongside the human.
func CallerActorSubject(ctx context.Context) string {
	return CallerFromContext(ctx).ActorSubject
}

// CallerActorChain returns the full RFC 8693 delegation chain subjects of a
// delegated caller, outermost actor first; nil for direct sessions.
func CallerActorChain(ctx context.Context) []string {
	return CallerFromContext(ctx).ActorChain
}
