package authz

import "slices"

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

// OrgDescriptor carries the CR-sourced parts of an OrgAccess — everything
// except Role, which the resolver derives from Grafana's membership lookup.
// Returned by OrgRegistry.List; the resolver joins these against
// per-caller role data to produce OrgAccess values.
type OrgDescriptor struct {
	Name        string
	DisplayName string
	OrgID       int64
	Tenants     []Tenant
	Datasources []Datasource
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
