package authz

import (
	"slices"
	"strings"
)

// TenantType is the authz-owned enum of what a tenant can access. Mirrors
// the observability-operator CRD's TenantType but lives here so tool
// handlers don't import the CRD package.
type TenantType string

const (
	// TenantTypeData grants read access to metrics and logs.
	TenantTypeData TenantType = "data"

	// TenantTypeAlerting grants access to rules and alerts.
	TenantTypeAlerting TenantType = "alerting"
)

// Tenant is the domain projection of a GrafanaOrganization.spec.tenants
// entry. Only the fields the MCP surface actually reads are carried.
type Tenant struct {
	Name  string
	Types []TenantType
}

// Datasource is the domain projection of a GrafanaOrganization.status.dataSources
// entry. Name is matched case-insensitively by FindDatasourceID.
type Datasource struct {
	ID   int64
	Name string
}

// Organization is the domain entity backing a Grafana org, plus the caller's
// Role once resolved.
//
// An Organization straight from OrgRegistry.List carries Role = RoleNone —
// that's "registry-output state, pre-resolution". The authorizer fills Role
// from the caller's Grafana membership before returning to tool handlers, so
// any Organization a handler sees has been authorised (Role ≥ RoleViewer).
// Code that calls OrgRegistry.List directly (the authorizer only) MUST NOT
// treat a RoleNone entry as authorised.
//
// Handlers read Organization values returned from the authorizer's Require /
// Resolve methods; those are always deep-cloned so handler mutations cannot
// escape into the cache — see cloneOrganization in cache.go.
type Organization struct {
	Name        string
	DisplayName string
	OrgID       int64
	Role        Role
	Tenants     []Tenant
	Datasources []Datasource
}

// HasTenantType returns true if any tenant on this org supports the given type
// (e.g. TenantTypeData or TenantTypeAlerting). Used to guard alerting-only
// tools.
func (o Organization) HasTenantType(want TenantType) bool {
	for _, t := range o.Tenants {
		if slices.Contains(t.Types, want) {
			return true
		}
	}
	return false
}

// FindDatasourceID picks the first datasource whose name (case-insensitively)
// contains all the given substrings. Returns (0, false) if none match.
// Used by tools to select the Mimir/Loki/Tempo/Alertmanager datasource
// without hard-coding IDs.
func (o Organization) FindDatasourceID(mustContain ...string) (int64, bool) {
	for _, ds := range o.Datasources {
		lower := strings.ToLower(ds.Name)
		match := true
		for _, needle := range mustContain {
			if !strings.Contains(lower, strings.ToLower(needle)) {
				match = false
				break
			}
		}
		if match {
			return ds.ID, true
		}
	}
	return 0, false
}

// cloneTenants returns a deep copy of a Tenant slice: the outer slice and
// each entry's Types slice are newly allocated so handler-side mutations
// cannot escape into the cache.
func cloneTenants(in []Tenant) []Tenant {
	if len(in) == 0 {
		return nil
	}
	out := make([]Tenant, len(in))
	for i, t := range in {
		out[i] = Tenant{Name: t.Name, Types: slices.Clone(t.Types)}
	}
	return out
}

// cloneDatasources returns a shallow copy of the slice; Datasource is
// value-only (no nested slices or pointers) so slices.Clone is enough.
func cloneDatasources(in []Datasource) []Datasource {
	if len(in) == 0 {
		return nil
	}
	return slices.Clone(in)
}
