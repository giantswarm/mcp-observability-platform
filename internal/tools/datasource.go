// Package tools — datasource.go: shared helpers for the local handlers
// that proxy through Grafana's datasource API (Tempo, Alertmanager v2).
// Delegated tools have their own UID resolution in gfBinder; this file
// is only for the local DatasourceProxy callers.
package tools

import (
	"context"
	"fmt"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// grafanaOpts packages orgID and caller-subject into a RequestOpts so
// every downstream call attributes to the caller via X-Grafana-User.
func grafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: authz.CallerSubject(ctx)}
}

// resolveDatasource runs the three checks every datasource-facing tool
// needs in one shot: the caller must have >= role on org, the org must
// host the required tenant type (empty = skip), and a datasource of
// the requested kind must exist. Errors are caller-ready strings so
// handlers can surface them unchanged.
func resolveDatasource(ctx context.Context, az authz.Authorizer, orgRef string, role authz.Role, tenantType authz.TenantType, kind grafana.DatasourceKind) (authz.Organization, int64, error) {
	org, err := az.RequireOrg(ctx, orgRef, role)
	if err != nil {
		return authz.Organization{}, 0, err
	}
	if tenantType != "" && !org.HasTenantType(tenantType) {
		return authz.Organization{}, 0, fmt.Errorf("org %q has no tenant of type %q — tool unavailable", orgRef, tenantType)
	}
	ds, ok := org.FindDatasource(kind)
	if !ok {
		return authz.Organization{}, 0, fmt.Errorf("org %q has no %s datasource", orgRef, kind)
	}
	return org, ds.ID, nil
}
