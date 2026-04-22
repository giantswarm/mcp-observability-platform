package authz

import (
	"slices"
	"strings"
)

// OrgAccess represents a caller's authorised access to one Grafana org.
// Fields are plain Go types; JSON tags live on tool-handler-side DTOs (when
// a handler marshals this, it translates to its own wire shape). Keeping
// the domain type tag-free avoids re-exporting Kubernetes CRD types through
// the package boundary.
//
// Returned values from Resolver are always cloned (Tenants + Datasources
// are deep-copied) so handler mutations cannot escape into the cache — see
// cloneOrgAccess in cache.go.
type OrgAccess struct {
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
func (o OrgAccess) HasTenantType(want TenantType) bool {
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
func (o OrgAccess) FindDatasourceID(mustContain ...string) (int64, bool) {
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

// descriptorToAccess builds an OrgAccess from an OrgDescriptor + role. The
// returned value *aliases* the descriptor's Tenants + Datasources slices;
// callers that hold on to it long-term must clone (see cloneOrgAccess).
func descriptorToAccess(d OrgDescriptor, role Role) OrgAccess {
	return OrgAccess{
		Name:        d.Name,
		DisplayName: d.DisplayName,
		OrgID:       d.OrgID,
		Role:        role,
		Tenants:     d.Tenants,
		Datasources: d.Datasources,
	}
}
