package authz

import "context"

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
	UserOrgs(ctx context.Context, userID int64) ([]Membership, error)
}

// Membership is the authz-internal projection of a Grafana org membership.
type Membership struct {
	OrgID int64
	Role  string // Grafana's role string
}
