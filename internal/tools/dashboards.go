// Package tools — dashboards.go: registration of the Grafana dashboard
// tools (search, summary, panel queries, JSON Pointer, annotations,
// deeplinks, run-panel-query, panel-image rendering). Handler bodies
// are kept inline because each is mostly argument-unpacking + a single
// helper call; the non-trivial logic lives in dashboards_panels.go
// (panel/template/JSON Pointer) and dashboards_summary.go (folder
// grouping, dashboard summarisation).
package tools

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// maxRenderedImageBytes caps the PNG payload returned by get_panel_image.
// Large renders (2000x1000 with dense data) easily hit multi-MB sizes and
// will blow past most LLM context windows even as base64. 4 MiB is a
// reasonable upper bound for practical panels.
const maxRenderedImageBytes = 4 * 1024 * 1024

func registerDashboardTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("search_dashboards",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List dashboards in a Grafana org, grouped by folder. Returns a compact tree {total, folders:[{title, dashboards:[{title,uid,url}]}]} so large orgs fit in the LLM context."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("query", mcp.Description("Optional title-substring filter applied server-side by Grafana.")),
			mcp.WithString("folder", mcp.Description("Return only dashboards in this folder title (case-insensitive). Use '' or '(no folder)' for root-level dashboards.")),
			mcp.WithNumber("limit", mcp.Description("Max results from Grafana before grouping (default 100, max 5000).")),
			mcp.WithNumber("page", mcp.Description("0-based page over folders when a filtered result is still large. Optional.")),
			mcp.WithNumber("pageSize", mcp.Description("Folder-page size (default 20, max 200). Optional.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.SearchDashboards(ctx, grafanaOpts(ctx, org.OrgID), req.GetString("query", ""), req.GetInt("limit", 0))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana search failed", err), nil
			}
			tree, err := groupDashboardsByFolder(body, req.GetString("folder", ""), req.GetInt("page", 0), req.GetInt("pageSize", 0))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboards", err), nil
			}
			return mcp.NewToolResultJSON(tree)
		},
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_by_uid",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Fetch a Grafana dashboard's full JSON. Prefer get_dashboard_summary or get_dashboard_property when you can — full dashboards are often 100s of KB and easily exceed the response cap. Use this only when you actually need the raw document."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID. See search_dashboards.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.GetDashboard(ctx, grafanaOpts(ctx, org.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_summary",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return a compact summary of a Grafana dashboard — title, tags, variables, row & panel layout — WITHOUT panel queries. Use this first to explore; then get_dashboard_panel_queries for the specific panel(s) you care about. Avoids pulling the full dashboard JSON (often 100s of KB)."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID. See search_dashboards.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.GetDashboard(ctx, grafanaOpts(ctx, org.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			summary, err := summariseDashboard(body)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboard", err), nil
			}
			return mcp.NewToolResultJSON(summary)
		},
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_panel_queries",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return the data queries (PromQL/LogQL/TraceQL) for one panel — or all panels, or a title-substring match. Use after get_dashboard_summary to pinpoint the exact panel. Returns the raw expressions so you can re-run them via query_prometheus / query_loki_logs / query_traces."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Description("Return only the panel with this id.")),
			mcp.WithString("titleContains", mcp.Description("Case-insensitive panel-title substring filter.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.GetDashboard(ctx, grafanaOpts(ctx, org.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			res, err := extractDashboardQueries(body, req.GetInt("panelId", 0), req.GetString("titleContains", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboard", err), nil
			}
			return mcp.NewToolResultJSON(res)
		},
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_property",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Read a specific sub-tree of a Grafana dashboard by JSON Pointer (RFC 6901). Example pointers: '/dashboard/panels' (all panels), '/dashboard/templating/list' (variables), '/dashboard/panels/0/targets' (first panel's queries). Much cheaper than fetching the full dashboard JSON when you know the path."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithString("path", mcp.Required(), mcp.Description("JSON Pointer, e.g. '/dashboard/title' or '/dashboard/panels/0'. Empty '' or '/' returns the whole document.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			path := req.GetString("path", "")
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.GetDashboard(ctx, grafanaOpts(ctx, org.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			sub, err := readJSONPointer(body, path)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("json pointer", err), nil
			}
			return mcp.NewToolResultText(string(sub)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("search_folders",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List folders visible in a Grafana org, optionally filtered by a title-substring query. Matches upstream grafana/mcp-grafana's search_folders; pair with search_dashboards to walk the dashboard tree by folder."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Description("Optional title-substring filter applied server-side by Grafana.")),
			mcp.WithNumber("limit", mcp.Description("Max folders returned (default 100, max 5000).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.SearchFolders(ctx, grafanaOpts(ctx, org.OrgID), req.GetString("query", ""), req.GetInt("limit", 0))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana search folders", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_annotation_tags",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List the set of tags used across Grafana annotations in an org, optionally filtered by a name prefix. Handy for discovering what tags to pass to get_annotations. Matches upstream grafana/mcp-grafana's get_annotation_tags."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("tag", mcp.Description("Optional tag-name prefix filter.")),
			mcp.WithNumber("limit", mcp.Description("Max tags returned (default 100).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			q := url.Values{}
			if tag := req.GetString("tag", ""); tag != "" {
				q.Set("tag", tag)
			}
			if limit := req.GetInt("limit", 0); limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			body, err := gc.GetAnnotationTags(ctx, grafanaOpts(ctx, org.OrgID), q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana annotation tags", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_annotations",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Read Grafana annotations (deploys, releases, manual notes) for a time window. Useful in RCA: 'what changed near 11:30?'. Filter by dashboard, panel, tags."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("from", mcp.Description("Unix ms or RFC3339; default now-1h.")),
			mcp.WithString("to", mcp.Description("Unix ms or RFC3339; default now.")),
			mcp.WithString("dashboardUid", mcp.Description("Restrict to one dashboard.")),
			mcp.WithNumber("panelId", mcp.Description("Restrict to one panel (requires dashboardUid).")),
			mcp.WithString("tags", mcp.Description("Comma-separated tag filter (annotations matching ALL tags).")),
			mcp.WithNumber("limit", mcp.Description("Max annotations (default 100, max 1000).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := url.Values{}
			q.Set("from", grafanaTimeArg(req, "from", -time.Hour))
			q.Set("to", grafanaTimeArg(req, "to", 0))
			if uid := req.GetString("dashboardUid", ""); uid != "" {
				q.Set("dashboardUID", uid)
			}
			if pid := req.GetInt("panelId", 0); pid > 0 {
				q.Set("panelId", strconv.Itoa(pid))
			}
			if tags := req.GetString("tags", ""); tags != "" {
				for t := range strings.SplitSeq(tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						q.Add("tags", t)
					}
				}
			}
			limit := req.GetInt("limit", 0)
			if limit <= 0 {
				limit = 100
			}
			limit = clampInt(limit, 1, 1000)
			q.Set("limit", strconv.Itoa(limit))
			body, err := gc.GetAnnotations(ctx, grafanaOpts(ctx, org.OrgID), q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana annotations", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("run_panel_query",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Run the stored query for a single dashboard panel directly — no need to extract+rebuild. Resolves the panel's datasource type (Mimir/Loki/Tempo) and routes through the appropriate proxy. Saves the get_dashboard_panel_queries → query_prometheus two-step."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Required(), mcp.Description("Panel id.")),
			mcp.WithNumber("targetIndex", mcp.Description("Which target/refId to run when the panel has multiple (default 0).")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch.")),
			mcp.WithString("step", mcp.Description("Step for range queries.")),
			mcp.WithNumber("limit", mcp.Description("Max log entries / traces for Loki/Tempo panels.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			panelID := req.GetInt("panelId", 0)
			if panelID <= 0 {
				return mcp.NewToolResultError("missing required argument 'panelId'"), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := gc.GetDashboard(ctx, grafanaOpts(ctx, org.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			panel, target, kind, vars, err := pickPanelTarget(body, panelID, req.GetInt("targetIndex", 0))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Build an invocation directly from the dashboard target + vars,
			// then dispatch via runDatasourceProxy. Leaves req.Params
			// read-only so the audit record captures what the LLM actually
			// sent, not the synthesised internal query.
			step := req.GetString("step", "")
			expanded := func(expr string) string {
				return expandGrafanaVars(expr, vars, req.GetString("start", ""), req.GetString("end", ""), step)
			}
			inv := datasourceInvocation{
				Org:      orgRef,
				Start:    req.GetString("start", ""),
				End:      req.GetString("end", ""),
				Step:     step,
				ExtraInt: req.GetInt("limit", 0),
			}
			switch kind {
			case dsKindMimir:
				if target.Expr == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no PromQL expression", panel.ID)), nil
				}
				inv.Query = expanded(target.Expr)
				return runDatasourceProxy(ctx, az, gc, datasourceSpec{
					Role: authz.RoleViewer, NeedTenant: authz.TenantTypeData, NameContains: []string{dsKindMimir},
					InstantPath: "api/v1/query", RangePath: "api/v1/query_range", QueryArg: "query", SupportsRange: true,
				}, inv)
			case dsKindLoki:
				if target.Expr == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no LogQL expression", panel.ID)), nil
				}
				inv.Query = expanded(target.Expr)
				// Loki's query_range requires start + end. The caller may or
				// may not have supplied them; default to the last hour when
				// absent. Doing this here instead of in runDatasourceProxy
				// keeps the shared proxy path free of per-datasource
				// defaulting logic.
				if inv.Start == "" {
					inv.Start = strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10)
				}
				if inv.End == "" {
					inv.End = strconv.FormatInt(time.Now().UnixNano(), 10)
				}
				return runDatasourceProxy(ctx, az, gc, datasourceSpec{
					Role: authz.RoleViewer, NeedTenant: authz.TenantTypeData, NameContains: []string{dsKindLoki},
					InstantPath: "loki/api/v1/query_range", RangePath: "loki/api/v1/query_range",
					QueryArg: "query", SupportsRange: true, ExtraArg: "limit",
				}, inv)
			case dsKindTempo:
				if target.Query == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no TraceQL query", panel.ID)), nil
				}
				inv.Query = expanded(target.Query)
				return runDatasourceProxy(ctx, az, gc, datasourceSpec{
					Role: authz.RoleViewer, NeedTenant: authz.TenantTypeData, NameContains: []string{dsKindTempo},
					InstantPath: "api/search", QueryArg: "q", ExtraArg: "limit",
				}, inv)
			default:
				return mcp.NewToolResultError(fmt.Sprintf("panel %d uses unsupported datasource kind %q (only mimir/loki/tempo)", panel.ID, kind)), nil
			}
		},
	)

	s.AddTool(
		mcp.NewTool("generate_deeplink",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Build a ready-to-share Grafana URL for a dashboard (or a single panel in view mode) with an embedded time range and optional template-variable values. Hand the URL back to a human operator."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Description("Open in view-panel mode if set.")),
			mcp.WithString("from", mcp.Description("Grafana time expression (e.g. 'now-1h', unix ms, RFC3339). Default 'now-1h'.")),
			mcp.WithString("to", mcp.Description("Grafana time expression. Default 'now'.")),
			mcp.WithObject("vars", mcp.Description("Template-variable values as {var: value} (e.g. {\"cluster\":\"prod-eu-1\"}).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			base := gc.BaseURL()
			from := req.GetString("from", "")
			if from == "" {
				from = "now-1h"
			}
			to := req.GetString("to", "")
			if to == "" {
				to = "now"
			}
			q := url.Values{}
			q.Set("orgId", strconv.FormatInt(org.OrgID, 10))
			q.Set("from", from)
			q.Set("to", to)
			if pid := req.GetInt("panelId", 0); pid > 0 {
				q.Set("viewPanel", strconv.Itoa(pid))
			}
			if vars, ok := req.GetArguments()["vars"].(map[string]any); ok {
				for k, v := range vars {
					if sv, ok := v.(string); ok {
						q.Set("var-"+k, sv)
					}
				}
			}
			link := base.JoinPath("/d/" + url.PathEscape(uid))
			link.RawQuery = q.Encode()
			return mcp.NewToolResultJSON(struct {
				URL string `json:"url"`
			}{URL: link.String()})
		},
	)

	s.AddTool(
		mcp.NewTool("get_panel_image",
			ReadOnlyAnnotation(),
			mcp.WithDescription(
				"Render a Grafana dashboard panel as a PNG image and return it as an MCP image resource. "+
					"Requires the 'grafana-image-renderer' plugin, or the standalone renderer service "+
					"(grafana/grafana-image-renderer) wired to Grafana via GF_RENDERING_SERVER_URL + "+
					"GF_RENDERING_CALLBACK_URL. Without the renderer, Grafana returns an HTML error and "+
					"this tool returns an actionable error message."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Required(), mcp.Description("Panel ID (from get_dashboard_summary or get_dashboard_panel_queries).")),
			mcp.WithString("from", mcp.Description("Grafana time (e.g. 'now-1h', unix ms, RFC3339). Default 'now-1h'.")),
			mcp.WithString("to", mcp.Description("Grafana time. Default 'now'.")),
			mcp.WithNumber("width", mcp.Description("Image width in px (default 1000).")),
			mcp.WithNumber("height", mcp.Description("Image height in px (default 500).")),
			mcp.WithString("theme", mcp.Description("'light' | 'dark' (default 'light').")),
			mcp.WithString("tz", mcp.Description("IANA timezone for time axis, e.g. 'Europe/Paris'. Default: UTC.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			uid, err := req.RequireString("uid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			panelID := req.GetInt("panelId", 0)
			if panelID <= 0 {
				return mcp.NewToolResultError("missing required argument 'panelId'"), nil
			}
			org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Rendering is CPU-heavy on the renderer service; give it room.
			ctx, cancel := withToolTimeout(ctx, 45*time.Second)
			defer cancel()

			// Short-circuit with a structured error when the plugin isn't
			// installed — without this, Grafana returns a PNG of its own
			// error message and the tool appears to succeed.
			if present, err := gc.HasImageRenderer(ctx); err == nil && !present {
				return mcp.NewToolResultJSON(struct {
					Error string `json:"error"`
					Hint  string `json:"hint"`
					Docs  string `json:"docs"`
				}{
					Error: "image_renderer_not_installed",
					Hint:  "ask your Grafana administrator to install the grafana-image-renderer plugin, or deploy the renderer service (grafana/grafana-image-renderer) and set GF_RENDERING_SERVER_URL + GF_RENDERING_CALLBACK_URL on Grafana",
					Docs:  "https://grafana.com/grafana/plugins/grafana-image-renderer/",
				})
			}

			q := url.Values{}
			q.Set("from", cmp.Or(req.GetString("from", ""), "now-1h"))
			q.Set("to", cmp.Or(req.GetString("to", ""), "now"))
			width := req.GetInt("width", 0)
			if width <= 0 {
				width = 1000
			}
			height := req.GetInt("height", 0)
			if height <= 0 {
				height = 500
			}
			q.Set("width", strconv.Itoa(width))
			q.Set("height", strconv.Itoa(height))
			if theme := req.GetString("theme", ""); theme != "" {
				q.Set("theme", theme)
			}
			if tz := req.GetString("tz", ""); tz != "" {
				q.Set("tz", tz)
			}
			q.Set("orgId", strconv.FormatInt(org.OrgID, 10))

			png, contentType, err := gc.RenderPanel(ctx, grafanaOpts(ctx, org.OrgID), uid, panelID, q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("render panel", err), nil
			}
			if len(png) > maxRenderedImageBytes {
				return mcp.NewToolResultJSON(struct {
					Error string `json:"error"`
					Bytes int    `json:"bytes"`
					Limit int    `json:"limit"`
					Hint  string `json:"hint"`
				}{
					Error: "image_too_large",
					Bytes: len(png),
					Limit: maxRenderedImageBytes,
					Hint:  "reduce width/height, narrow the time range, or use get_dashboard_panel_queries + query_prometheus to summarise numerically",
				})
			}
			// MCP ImageContent uses base64-encoded data.
			return mcp.NewToolResultImage(
				"panel render: dashboard "+uid+" panel "+strconv.Itoa(panelID),
				base64.StdEncoding.EncodeToString(png),
				contentType,
			), nil
		},
	)
}
