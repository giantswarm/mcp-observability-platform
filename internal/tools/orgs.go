// orgs.go — list_orgs (local) plus list_datasources / get_datasource
// (delegated). list_orgs surfaces our GrafanaOrganization CR access
// matrix and has no upstream equivalent. list_datasources /
// get_datasource go through gfBinder.bindOrgTool.
package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func registerOrgTools(s *mcpsrv.MCPServer, disabled map[string]bool, az authz.Authorizer, b *gfBinder) {
	maybeAddTool(s, disabled,
		mcp.NewTool("list_orgs",
			readOnlyAnnotation(),
			mcp.WithDescription("List orgs the caller can access. Call this first to discover the 'org' value other tools need. Returns name, displayName, orgId, role, and tenantTypes (e.g. [\"data\",\"alerting\"])."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			type item struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				OrgID       int64    `json:"orgId"`
				Role        string   `json:"role"`
				TenantTypes []string `json:"tenantTypes"`
			}
			type response struct {
				Orgs    []item `json:"orgs"`
				Message string `json:"message,omitempty"`
			}

			access, err := az.ListOrgs(ctx)
			if errors.Is(err, authz.ErrCallerUnknownToGrafana) {
				// First-login UX: tell the caller to register with Grafana
				// instead of returning an opaque "not authorised" or empty
				// list — both are misleading.
				return mcp.NewToolResultJSON(response{
					Orgs:    []item{},
					Message: err.Error(),
				})
			}
			if err != nil {
				return mcp.NewToolResultErrorFromErr("resolver failed", err), nil
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
			return mcp.NewToolResultJSON(response{Orgs: out})
		},
	)

	// Datasource tools delegate to upstream verbatim. We add only the
	// `org` argument; upstream defines everything else (limit, type
	// filter, uid, etc.) on its own schema.
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.ListDatasources,
		mcpgrafanatools.GetDatasource,
	} {
		b.bindOrgTool(s, authz.RoleViewer, t)
	}
}
