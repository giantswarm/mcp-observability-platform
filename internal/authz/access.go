package authz

import (
	"slices"
	"strings"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
)

// OrgAccess represents a caller's authorised access to one Grafana org.
// Fields carry JSON tags so this struct can be marshaled directly into MCP
// tool and resource responses.
//
// Returned values from Resolver are always cloned (Tenants + Datasources
// are deep-copied via DeepCopyInto) so handler mutations cannot escape
// into the cache — see cloneOrgAccess in cache.go.
type OrgAccess struct {
	Name        string                     `json:"name"`
	DisplayName string                     `json:"displayName"`
	OrgID       int64                      `json:"orgID"`
	Role        Role                       `json:"role"`
	Tenants     []obsv1alpha2.TenantConfig `json:"tenants"`
	Datasources []obsv1alpha2.DataSource   `json:"datasources"`
}

// HasTenantType returns true if any tenant on this org supports the given type
// (e.g. "alerting" or "data"). Used to guard alerting-only tools.
func (o OrgAccess) HasTenantType(want obsv1alpha2.TenantType) bool {
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

// toOrgAccess builds an OrgAccess from a CR + role. The returned value
// *aliases* the CR's Tenants + Datasources slices; callers that hold on to
// it long-term must clone (see cloneOrgAccess). Inside load() the alias is
// fine because the CR list is only live for the function call and the
// resulting OrgAccess is stored in the cache under aliased ownership.
func toOrgAccess(cr *obsv1alpha2.GrafanaOrganization, role Role) OrgAccess {
	return OrgAccess{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		OrgID:       cr.Status.OrgID,
		Role:        role,
		Tenants:     cr.Spec.Tenants,
		Datasources: cr.Status.DataSources,
	}
}
