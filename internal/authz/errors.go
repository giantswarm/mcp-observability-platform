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

	// ErrNoCallerIdentity means the authorizer was called without any of
	// Email or Subject set on Caller.
	ErrNoCallerIdentity = errors.New("no caller identity")
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
