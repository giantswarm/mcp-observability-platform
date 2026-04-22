// Package tools — orgs.go: org + datasource list/read tools (list_orgs, list_datasources, get_datasource).
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/identity"
)

func registerOrgTools(s *mcpsrv.MCPServer, d *Deps) {
	s.AddTool(
		mcp.NewTool("list_orgs",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List the Grafana organizations you have access to, with your role and available tenants."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			access, err := d.Resolver.Resolve(ctx, identity.CallerAuthz(ctx))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("resolver failed", err), nil
			}
			// Minimal projection keeps list_orgs well under the response cap
			// even for callers with 50+ orgs. Full datasource info is
			// available per-org via list_datasources + get_datasource.
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
			sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
			return mcp.NewToolResultJSON(struct {
				Orgs []item `json:"orgs"`
			}{Orgs: out})
		},
	)

	s.AddTool(
		mcp.NewTool("list_datasources",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List the Grafana datasources visible in an org, with name/type/uid. Tools like query_prometheus pick a datasource by name substring; use this to see the full list if a selection fails."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, err := d.Resolver.Require(ctx, identity.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := d.Grafana.ListDatasources(ctx, grafanaOpts(ctx, oa.OrgID))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana /api/datasources failed", err), nil
			}
			// Grafana returns ~20 fields per datasource; we project down to
			// the fields the MCP surface actually needs. Cuts the payload
			// from ~2KB/DS to ~200B/DS.
			type raw_ struct {
				ID        int64  `json:"id"`
				UID       string `json:"uid"`
				Name      string `json:"name"`
				Type      string `json:"type"`
				Access    string `json:"access"`
				URL       string `json:"url"`
				IsDefault bool   `json:"isDefault"`
			}
			var in []raw_
			if err := json.Unmarshal(raw, &in); err != nil {
				return mcp.NewToolResultErrorFromErr("parse datasources", err), nil
			}
			type item struct {
				ID        int64  `json:"id"`
				UID       string `json:"uid"`
				Name      string `json:"name"`
				Type      string `json:"type"`
				IsDefault bool   `json:"isDefault,omitempty"`
			}
			out := make([]item, 0, len(in))
			for _, d := range in {
				out = append(out, item{ID: d.ID, UID: d.UID, Name: d.Name, Type: d.Type, IsDefault: d.IsDefault})
			}
			sort.Slice(out, func(i, j int) bool {
				if out[i].Type != out[j].Type {
					return out[i].Type < out[j].Type
				}
				return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
			})
			return mcp.NewToolResultJSON(struct {
				Org         string `json:"org"`
				Total       int    `json:"total"`
				Datasources []item `json:"datasources"`
			}{Org: org, Total: len(out), Datasources: out})
		},
	)

	s.AddTool(
		mcp.NewTool("get_datasource",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return full Grafana datasource details by UID. Use after list_datasources to inspect access mode, JSON settings, etc."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Datasource UID. See list_datasources.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, err := d.Resolver.Require(ctx, identity.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDatasource(ctx, grafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana datasource", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)
}
