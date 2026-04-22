package authz

import (
	"context"
	"net/http"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"
)

// Caller carries the identity bits the resolver needs to ask Grafana about
// someone. Email is the human-facing handle Grafana provisions users by;
// Subject is the OIDC sub claim, the stable non-spoofable identifier used
// as the cache key.
type Caller struct {
	Email   string
	Subject string
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

// Empty reports whether no identifying fields were set.
func (c Caller) Empty() bool { return c.Email == "" && c.Subject == "" }

// OrgRegistry is the resolver's port onto "the set of known Grafana
// organisations". Implementations today wrap controller-runtime's informer
// cache of GrafanaOrganization CRs; tests implement it directly in-memory.
// Domain types only — the adapter is responsible for translating CR shapes
// into Organization, so tests need no CR imports.
type OrgRegistry interface {
	List(ctx context.Context) ([]Organization, error)
}

// OrgMembershipLookup is the subset of grafana.Client the resolver needs.
// Named from the consumer's perspective — the name doesn't leak "this is
// satisfied by a Grafana client" because the resolver shouldn't care.
type OrgMembershipLookup interface {
	// LookupUserID returns Grafana's internal user id for the given email or
	// login, or (0, false, nil) if the user hasn't been provisioned yet.
	LookupUserID(ctx context.Context, loginOrEmail string) (id int64, found bool, err error)

	// UserOrgs returns the (orgID, roleString) pairs Grafana has computed
	// for the given user. roleString is one of "Admin" | "Editor" | "Viewer"
	// | "None" (Grafana's own strings).
	UserOrgs(ctx context.Context, userID int64) ([]OrgMembership, error)
}

// OrgMembership is the authz-internal projection of a Grafana org membership.
type OrgMembership struct {
	OrgID int64
	Role  string // Grafana's role string
}

// callerKey is an unexported context key for the caller's validated
// identity. Unexported so external packages cannot overwrite the value.
type callerKey struct{}

// withCaller attaches the caller identity to ctx. Nil UserInfo is a no-op —
// downstream accessors check presence explicitly.
func withCaller(ctx context.Context, ui *providers.UserInfo) context.Context {
	if ui == nil {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, ui)
}

// userInfoFromContext returns the raw UserInfo attached by PromoteOAuthCaller.
// Kept unexported: external callers should use CallerSubject /
// CallerTokenSource / CallerFromContext for the specific bits they need.
func userInfoFromContext(ctx context.Context) (*providers.UserInfo, bool) {
	ui, ok := ctx.Value(callerKey{}).(*providers.UserInfo)
	return ui, ok
}

// CallerSubject returns a stable identifier for the caller — provider ID
// preferred over email; empty when no identity is attached. Used for audit
// logs and the X-Grafana-User header so Grafana's audit log shows who-did-
// what instead of the server-admin SA.
func CallerSubject(ctx context.Context) string {
	ui, ok := userInfoFromContext(ctx)
	if !ok || ui == nil {
		return ""
	}
	if ui.ID != "" {
		return ui.ID
	}
	return ui.Email
}

// CallerTokenSource returns the OAuth flavour that produced the caller's
// identity: "oauth" for tokens minted by our own /oauth/token endpoint,
// "sso" for ID tokens forwarded via TrustedAudiences, "" when no identity
// is attached. Recorded on audit entries so direct and SSO-forwarded
// sessions are distinguishable.
func CallerTokenSource(ctx context.Context) string {
	ui, ok := userInfoFromContext(ctx)
	if !ok || ui == nil {
		return ""
	}
	return string(ui.TokenSource)
}

// CallerFromContext extracts the identifiers the resolver needs to ask
// Grafana who this caller is. Returns an empty Caller if no identity is
// attached; the resolver then errors downstream via ErrNoCallerIdentity.
//
// Subject is the OIDC sub claim. Login is deliberately left empty: OIDC sub
// is NOT a Grafana login name, and collapsing the two here would make
// Grafana's /api/users/lookup silently miss when the caller's email/login
// doesn't match their sub. The resolver falls back to Email-based lookup
// when Login is empty.
func CallerFromContext(ctx context.Context) Caller {
	ui, ok := userInfoFromContext(ctx)
	if !ok || ui == nil {
		return Caller{}
	}
	return Caller{Email: ui.Email, Subject: ui.ID}
}

// PromoteOAuthCaller lifts the UserInfo attached by mcp-oauth's ValidateToken
// middleware onto the context mcp-go passes to tool handlers. Intended as
// the argument to mcpsrv.WithHTTPContextFunc / WithSSEContextFunc — it
// bridges HTTP-level OAuth state into MCP-level handler context. Callers
// that reach a tool without a valid identity are rejected at the authz
// boundary (the resolver returns ErrNoCallerIdentity on an empty Caller).
func PromoteOAuthCaller(ctx context.Context, r *http.Request) context.Context {
	if ui, ok := oauth.UserInfoFromContext(r.Context()); ok {
		return withCaller(ctx, ui)
	}
	return ctx
}
