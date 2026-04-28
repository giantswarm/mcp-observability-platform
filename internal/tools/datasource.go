// Package tools — datasource.go: shared helpers for the local handlers
// that proxy through Grafana's datasource API (Tempo, Alertmanager v2).
// Delegated tools have their own UID resolution in gfBinder; this file
// is only for the local DatasourceProxy callers.
package tools

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// grafanaOpts builds a RequestOpts that attributes the call to the
// OIDC subject via X-Grafana-User in Grafana's audit log.
func grafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: authz.CallerSubject(ctx)}
}

// resolveDatasource reads "org" from req, runs the three checks every
// datasource-facing tool needs (role on org, tenant type, datasource
// of kind), and returns (org, dsID). Errors are caller-ready strings
// so handlers can surface them unchanged via mcp.NewToolResultError.
func resolveDatasource(ctx context.Context, az authz.Authorizer, req mcp.CallToolRequest, role authz.Role, tenantType authz.TenantType, kind grafana.DatasourceKind) (authz.Organization, int64, error) {
	orgRef, err := req.RequireString("org")
	if err != nil {
		return authz.Organization{}, 0, err
	}
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
