// Package tools — orgs.go: org + datasource tools.
//
// list_orgs is permanently local — it surfaces our GrafanaOrganization
// CR access matrix (name, displayName, orgID, role, tenantTypes) and
// has no upstream equivalent. list_datasources / get_datasource
// delegate to upstream grafana/mcp-grafana via the bridge.
package tools

import (
	"context"
	"sort"
	"strings"

	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

func registerOrgTools(s *mcpsrv.MCPServer, az authz.Authorizer, br *upstream.Bridge) {
	s.AddTool(
		mcp.NewTool("list_orgs",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List the Grafana organizations you have access to, with your role and available tenants."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			access, err := az.ListOrgs(ctx)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("resolver failed", err), nil
			}
			// Minimal projection keeps list_orgs well under the response
			// cap even for callers with 50+ orgs. Full datasource info
			// is available per-org via list_datasources + get_datasource.
			type item struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				OrgID       int64    `json:"orgID"`
				Role        string   `json:"role"`
				TenantTypes []string `json:"tenantTypes"`
			}
			out := make([]item, 0, len(access))
			for _, a := range access {
				tt := map[string]struct{}{}
				for _, tenant := range a.Tenants {
					for _, t := range tenant.Types {
						tt[string(t)] = struct{}{}
					}
				}
				types := make([]string, 0, len(tt))
				for t := range tt {
					types = append(types, t)
				}
				sort.Strings(types)
				out = append(out, item{
					Name:        a.Name,
					DisplayName: a.DisplayName,
					OrgID:       a.OrgID,
					Role:        a.Role.String(),
					TenantTypes: types,
				})
			}
			sort.Slice(out, func(i, j int) bool {
				return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName)
			})
			return mcp.NewToolResultJSON(struct {
				Orgs []item `json:"orgs"`
			}{Orgs: out})
		},
	)

	// Datasource tools delegate to upstream verbatim. We add only the
	// `org` argument; upstream defines everything else (limit, type
	// filter, uid, etc.) on its own schema.
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.ListDatasources,
		mcpgrafanatools.GetDatasource,
	} {
		s.AddTool(upstream.WithOrg(t.Tool), br.Wrap(authz.RoleViewer, t))
	}
}
