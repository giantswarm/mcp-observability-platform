package authz

import (
	"errors"
	"fmt"
)

// Sentinel errors for callers that want to distinguish "this org doesn't
// exist" from "this org exists but you can't access it."
var (
	// ErrOrgNotFound means no GrafanaOrganization CR matches the orgRef
	// (neither by Name nor by DisplayName). Wrappable via errors.Is.
	ErrOrgNotFound = errors.New("org not found")

	// ErrNoCallerIdentity means the authorizer was called without an OIDC
	// subject on Caller. Email-only identities are rejected: email is
	// caller-mutable in some IdPs, subject is the stable security
	// identifier and is the only key the resolver-cache trusts.
	ErrNoCallerIdentity = errors.New("no caller identity")

	// ErrAmbiguousOrgRef means orgRef matches more than one registered
	// Organization by DisplayName. Returned instead of silently picking
	// one — collisions need an operator fix, not a coin flip.
	ErrAmbiguousOrgRef = errors.New("ambiguous org reference")

	// ErrInvalidMinRole means RequireOrg was called with minRole=RoleNone,
	// which is a vacuous gate (every Role.AtLeast(RoleNone) is true).
	// Pass RoleViewer for "any access at all".
	ErrInvalidMinRole = errors.New("minRole must be RoleViewer or higher")
)

// ErrNotAuthorised signals that Grafana does not grant the caller access
// to the referenced org. Message is caller-ready.
func ErrNotAuthorised(org string) error {
	return fmt.Errorf("not authorised for org %q", org)
}

// ErrInsufficientRole signals the caller has access but below the required
// role. Message carries both the actual and required role for diagnostics.
func ErrInsufficientRole(org string, have, need Role) error {
	return fmt.Errorf("insufficient role for org %q: have %s, need %s", org, have, need)
}
