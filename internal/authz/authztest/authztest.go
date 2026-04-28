// Package authztest provides a single Authorizer fake shared by the
// server, tools, and binder tests. Centralised so the implementation of
// the Authorizer contract has one place to evolve when the interface
// changes.
package authztest

import (
	"context"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// Fake satisfies authz.Authorizer for tests.
//
// RequireOrg behaviour:
//   - Err set         → returned as-is (use to inject failures).
//   - OrgsByRef set   → looked up by orgRef; missing key returns ErrOrgNotFound.
//   - otherwise       → returns Org unconditionally.
//
// ListOrgs returns OrgsByRef when set, otherwise a single-entry map keyed
// on Org.Name (or nil for the zero Org).
//
// GotRef / GotMin record the last RequireOrg arguments — used by binder
// tests that assert the binder passed the right org reference and minRole
// down to the authorizer.
type Fake struct {
	Org       authz.Organization
	OrgsByRef map[string]authz.Organization
	Err       error

	GotRef string
	GotMin authz.Role
}

func (f *Fake) RequireOrg(_ context.Context, ref string, min authz.Role) (authz.Organization, error) {
	f.GotRef = ref
	f.GotMin = min
	if f.Err != nil {
		return authz.Organization{}, f.Err
	}
	if f.OrgsByRef != nil {
		org, ok := f.OrgsByRef[ref]
		if !ok {
			return authz.Organization{}, authz.ErrOrgNotFound
		}
		return org, nil
	}
	return f.Org, nil
}

func (f *Fake) ListOrgs(_ context.Context) (map[string]authz.Organization, error) {
	if f.OrgsByRef != nil {
		out := make(map[string]authz.Organization, len(f.OrgsByRef))
		for _, o := range f.OrgsByRef {
			out[o.Name] = o
		}
		return out, nil
	}
	if f.Org.Name == "" {
		return nil, nil
	}
	return map[string]authz.Organization{f.Org.Name: f.Org}, nil
}
