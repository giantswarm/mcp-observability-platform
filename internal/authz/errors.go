package authz

import (
	"errors"
	"fmt"
)

// Sentinel errors. Use errors.Is to classify denial reasons; the wrapping
// helpers below carry the org name and roles for human-readable messages.
var (
	// ErrOrgNotFound means no GrafanaOrganization CR matches the orgRef
	// (neither by Name nor by DisplayName).
	ErrOrgNotFound = errors.New("org not found")

	// ErrNoCallerIdentity means the authorizer was called without an OIDC
	// subject on Caller. Email-only identities are rejected: email is
	// caller-mutable in some IdPs, subject is the stable security
	// identifier and is the only key the resolver-cache trusts.
	ErrNoCallerIdentity = errors.New("no caller identity")

	// ErrCallerUnknownToGrafana means the caller authenticated with our IdP
	// but has never logged into Grafana yet, so Grafana has no user record.
	// Distinct from ErrNotAuthorised: the latter means "Grafana knows you,
	// but not for this org"; this one means "Grafana doesn't know you yet —
	// log in once and retry."
	ErrCallerUnknownToGrafana = errors.New("caller has no Grafana account yet — log into Grafana once to register")

	// ErrAmbiguousOrgRef means orgRef matches more than one registered
	// Organization by DisplayName. Returned instead of silently picking
	// one — collisions need an operator fix, not a coin flip.
	ErrAmbiguousOrgRef = errors.New("ambiguous org reference")

	// ErrInvalidMinRole means RequireOrg was called with minRole=RoleNone,
	// which is a vacuous gate (every Role.AtLeast(RoleNone) is true).
	// Pass RoleViewer for "any access at all".
	ErrInvalidMinRole = errors.New("minRole must be RoleViewer or higher")

	// ErrNotAuthorised means Grafana does not grant the caller access to
	// the referenced org. Wrap via notAuthorisedFor for human-readable
	// messages; classify via errors.Is.
	ErrNotAuthorised = errors.New("not authorised for org")

	// ErrInsufficientRole means the caller has access but below the
	// required role. Wrap via insufficientRoleFor.
	ErrInsufficientRole = errors.New("insufficient role for org")
)

func notAuthorisedFor(org string) error {
	return fmt.Errorf("%w %q", ErrNotAuthorised, org)
}

func insufficientRoleFor(org string, have, need Role) error {
	return fmt.Errorf("%w %q: have %s, need %s", ErrInsufficientRole, org, have, need)
}
