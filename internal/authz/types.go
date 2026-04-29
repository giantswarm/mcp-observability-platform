package authz

import (
	"slices"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
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

// Organization is the domain entity backing a Grafana org, plus the caller's
// Role once resolved.
//
// An Organization straight from OrgLister.List carries Role = RoleNone
// (pre-resolution). The authorizer fills Role from the caller's Grafana
// role assignment before returning to tool handlers, so any Organization
// a handler sees has been authorised (Role ≥ RoleViewer). Code that
// calls OrgLister.List directly (the authorizer only) MUST NOT treat a
// RoleNone entry as authorised.
//
// Handlers receive Organization values from RequireOrg / ListOrgs;
// those are always deep-cloned so handler mutations cannot escape
// into the cache — see cloneOrganization in cache.go.
type Organization struct {
	Name        string
	DisplayName string
	OrgID       int64
	Role        Role
	Tenants     []Tenant
	Datasources []grafana.Datasource
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

// FindDatasource picks the datasource backing the given kind via
// substring matching on Datasource.Name (see grafana.MatchKind).
func (o Organization) FindDatasource(kind grafana.DatasourceKind) (grafana.Datasource, bool) {
	return grafana.MatchKind(o.Datasources, kind)
}

// cloneTenants deep-copies the slice and each entry's Types slice.
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
