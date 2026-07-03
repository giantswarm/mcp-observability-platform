package authz

import "context"

// Caller carries the identity bits the authorizer needs to ask Grafana about
// someone, plus the audit attributes downstream middleware logs. Subject is
// the OIDC sub claim — the stable, non-spoofable identifier; the cache keys
// on it. Email is the human-facing handle Grafana provisions OAuth users
// by. TokenSource records the OAuth flavour ("oauth" / "sso") so audit
// logs can distinguish direct sessions from SSO-forwarded ones.
//
// A valid Caller MUST have a non-empty Subject — see Authenticated().
//
// ActorSubject and ActorChain carry the RFC 8693 act claim of a delegated
// (on-behalf-of) token: the agent acting for the human, outermost actor
// first. Audit-only — authorization runs on the human subject's identity.
type Caller struct {
	Subject      string
	Email        string
	TokenSource  string
	ActorSubject string
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

// Authenticated reports whether the caller carries a usable identity. A
// caller with no Subject is unauthenticated even if Email is set: email is
// mutable in some IdPs and isn't safe to use as the cache key. Two callers
// with the same email and no subject would otherwise collide on one cache
// slot.
func (c Caller) Authenticated() bool { return c.Subject != "" }

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
