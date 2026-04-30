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

// resolveDatasource reads "org" from req, runs authz + tenant-type
// gating, then resolves the target datasource via live ListDatasources:
// caller-supplied datasource_uid overrides; otherwise the first DS of
// dsType wins. Returns (org, dsID). Errors are caller-ready strings so
// handlers can surface them unchanged via mcp.NewToolResultError.
func resolveDatasource(ctx context.Context, az authz.Authorizer, gc grafana.Client, req mcp.CallToolRequest, role authz.Role, tenantType authz.TenantType, dsType grafana.DatasourceType) (authz.Organization, int64, error) {
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
	opts := grafanaOpts(ctx, org.OrgID)
	if uid := req.GetString(datasourceUIDArg, ""); uid != "" {
		ds, err := gc.LookupDatasourceByUID(ctx, opts, uid)
		if err != nil {
			return authz.Organization{}, 0, fmt.Errorf("%s: %w", datasourceUIDArg, err)
		}
		if !grafana.MatchesType(ds, dsType) {
			return authz.Organization{}, 0, fmt.Errorf("datasource %q is type %q, not %s", uid, ds.Type, dsType)
		}
		return org, ds.ID, nil
	}
	dss, err := gc.ListDatasources(ctx, opts)
	if err != nil {
		return authz.Organization{}, 0, fmt.Errorf("list datasources: %w", err)
	}
	matches := grafana.FilterDatasourcesByType(dss, dsType)
	if len(matches) == 0 {
		return authz.Organization{}, 0, fmt.Errorf("org %q has no %s datasource", orgRef, dsType)
	}
	return org, matches[0].ID, nil
}
