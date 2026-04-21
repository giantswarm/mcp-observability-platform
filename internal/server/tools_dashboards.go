package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/middleware"
)

func registerDashboardTools(s *mcpsrv.MCPServer, d *middleware.Deps) {
	s.AddTool(
		mcp.NewTool("list_dashboards",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("List dashboards in a Grafana org, grouped by folder. Returns a compact tree {total, folders:[{title, dashboards:[{title,uid,url}]}]} so large orgs fit in the LLM context."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("query", mcp.Description("Optional title-substring filter applied server-side by Grafana.")),
			mcp.WithString("folder", mcp.Description("Return only dashboards in this folder title (case-insensitive). Use '' or '(no folder)' for root-level dashboards.")),
			mcp.WithNumber("limit", mcp.Description("Max results from Grafana before grouping (default 100, max 5000).")),
			mcp.WithNumber("page", mcp.Description("0-based page over folders when a filtered result is still large. Optional.")),
			mcp.WithNumber("pageSize", mcp.Description("Folder-page size (default 20, max 200). Optional.")),
		),
		middleware.Handle("list_dashboards", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.SearchDashboards(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), middleware.StrArg(args, "query"), middleware.IntArg(args, "limit"))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana search failed", err), nil
			}
			tree, err := groupDashboardsByFolder(body, middleware.StrArg(args, "folder"), middleware.IntArg(args, "page"), middleware.IntArg(args, "pageSize"))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboards", err), nil
			}
			return mcp.NewToolResultJSON(tree)
		}),
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_by_uid",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Fetch a Grafana dashboard's full JSON. Prefer get_dashboard_summary or get_dashboard_property when you can — full dashboards are often 100s of KB and easily exceed the response cap. Use this only when you actually need the raw document."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID. See list_dashboards.")),
		),
		middleware.Handle("get_dashboard_by_uid", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDashboard(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			if capErr := middleware.EnforceResponseCap(body); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			return mcp.NewToolResultText(string(body)), nil
		}),
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_summary",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Return a compact summary of a Grafana dashboard — title, tags, variables, row & panel layout — WITHOUT panel queries. Use this first to explore; then get_dashboard_panel_queries for the specific panel(s) you care about. Avoids pulling the full dashboard JSON (often 100s of KB)."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID. See list_dashboards.")),
		),
		middleware.Handle("get_dashboard_summary", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDashboard(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			summary, err := summariseDashboard(body)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboard", err), nil
			}
			return mcp.NewToolResultJSON(summary)
		}),
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_panel_queries",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Return the data queries (PromQL/LogQL/TraceQL) for one panel — or all panels, or a title-substring match. Use after get_dashboard_summary to pinpoint the exact panel. Returns the raw expressions so you can re-run them via query_metrics / query_logs / query_traces."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Description("Return only the panel with this id.")),
			mcp.WithString("titleContains", mcp.Description("Case-insensitive panel-title substring filter.")),
		),
		middleware.Handle("get_dashboard_panel_queries", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDashboard(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			res, err := extractDashboardQueries(body, middleware.IntArg(args, "panelId"), middleware.StrArg(args, "titleContains"))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse dashboard", err), nil
			}
			return mcp.NewToolResultJSON(res)
		}),
	)

	s.AddTool(
		mcp.NewTool("get_dashboard_property",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Read a specific sub-tree of a Grafana dashboard by JSON Pointer (RFC 6901). Example pointers: '/dashboard/panels' (all panels), '/dashboard/templating/list' (variables), '/dashboard/panels/0/targets' (first panel's queries). Much cheaper than fetching the full dashboard JSON when you know the path."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithString("path", mcp.Required(), mcp.Description("JSON Pointer, e.g. '/dashboard/title' or '/dashboard/panels/0'. Empty '' or '/' returns the whole document.")),
		),
		middleware.Handle("get_dashboard_property", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			path := middleware.StrArg(args, "path")
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDashboard(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			sub, err := readJSONPointer(body, path)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("json pointer", err), nil
			}
			if capErr := middleware.EnforceResponseCap(sub); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			return mcp.NewToolResultText(string(sub)), nil
		}),
	)

	s.AddTool(
		mcp.NewTool("get_annotations",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Read Grafana annotations (deploys, releases, manual notes) for a time window. Useful in RCA: 'what changed near 11:30?'. Filter by dashboard, panel, tags."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("from", mcp.Description("Unix ms or RFC3339; default now-1h.")),
			mcp.WithString("to", mcp.Description("Unix ms or RFC3339; default now.")),
			mcp.WithString("dashboardUid", mcp.Description("Restrict to one dashboard.")),
			mcp.WithNumber("panelId", mcp.Description("Restrict to one panel (requires dashboardUid).")),
			mcp.WithString("tags", mcp.Description("Comma-separated tag filter (annotations matching ALL tags).")),
			mcp.WithNumber("limit", mcp.Description("Max annotations (default 100, max 1000).")),
		),
		middleware.Handle("get_annotations", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := url.Values{}
			q.Set("from", grafanaTimeArg(args, "from", -time.Hour))
			q.Set("to", grafanaTimeArg(args, "to", 0))
			if uid := middleware.StrArg(args, "dashboardUid"); uid != "" {
				q.Set("dashboardUID", uid)
			}
			if pid := middleware.IntArg(args, "panelId"); pid > 0 {
				q.Set("panelId", strconv.Itoa(pid))
			}
			if tags := middleware.StrArg(args, "tags"); tags != "" {
				for _, t := range strings.Split(tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						q.Add("tags", t)
					}
				}
			}
			limit := middleware.ClampInt(middleware.IntArg(args, "limit"), 1, 1000)
			if limit == 0 {
				limit = 100
			}
			q.Set("limit", strconv.Itoa(limit))
			body, err := d.Grafana.GetAnnotations(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana annotations", err), nil
			}
			if capErr := middleware.EnforceResponseCap(body); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			return mcp.NewToolResultText(string(body)), nil
		}),
	)

	s.AddTool(
		mcp.NewTool("run_panel_query",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Run the stored query for a single dashboard panel directly — no need to extract+rebuild. Resolves the panel's datasource type (Mimir/Loki/Tempo) and routes through the appropriate proxy. Saves the get_dashboard_panel_queries → query_metrics two-step."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Required(), mcp.Description("Panel id.")),
			mcp.WithNumber("targetIndex", mcp.Description("Which target/refId to run when the panel has multiple (default 0).")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch.")),
			mcp.WithString("step", mcp.Description("Step for range queries.")),
			mcp.WithNumber("limit", mcp.Description("Max log entries / traces for Loki/Tempo panels.")),
		),
		middleware.Handle("run_panel_query", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			panelID := middleware.IntArg(args, "panelId")
			if panelID <= 0 {
				return mcp.NewToolResultError("missing required argument 'panelId'"), nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := d.Grafana.GetDashboard(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("grafana get dashboard failed", err), nil
			}
			panel, target, kind, vars, err := pickPanelTarget(body, panelID, middleware.IntArg(args, "targetIndex"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Re-dispatch to the appropriate datasource handler by synthesising
			// a tool call against the correct kind (mimir/loki/tempo).
			newArgs := map[string]any{"org": org}
			for _, k := range []string{"start", "end", "step", "limit"} {
				if v, ok := args[k]; ok {
					newArgs[k] = v
				}
			}
			step := middleware.StrArg(args, "step")
			expanded := func(expr string) string {
				return expandGrafanaVars(expr, vars, middleware.StrArg(args, "start"), middleware.StrArg(args, "end"), step)
			}
			switch kind {
			case "mimir":
				if target.Expr == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no PromQL expression", panel.ID)), nil
				}
				newArgs["query"] = expanded(target.Expr)
				req.Params.Arguments = newArgs
				return middleware.DatasourceProxyHandler(d, middleware.DatasourceSpec{
					Role: authz.RoleViewer, NeedTenant: obsv1alpha2.TenantTypeData, NameContains: []string{"mimir"},
					InstantPath: "api/v1/query", RangePath: "api/v1/query_range", QueryArg: "query", SupportsRange: true,
				})(ctx, req)
			case "loki":
				if target.Expr == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no LogQL expression", panel.ID)), nil
				}
				newArgs["query"] = expanded(target.Expr)
				req.Params.Arguments = newArgs
				return middleware.DatasourceProxyHandler(d, middleware.DatasourceSpec{
					Role: authz.RoleViewer, NeedTenant: obsv1alpha2.TenantTypeData, NameContains: []string{"loki"},
					InstantPath: "loki/api/v1/query_range", RangePath: "loki/api/v1/query_range",
					QueryArg: "query", SupportsRange: true, ForceRange: true, DefaultRangeAgo: time.Hour, ExtraArg: "limit",
				})(ctx, req)
			case "tempo":
				if target.Query == "" {
					return mcp.NewToolResultError(fmt.Sprintf("panel %d target has no TraceQL query", panel.ID)), nil
				}
				newArgs["query"] = expanded(target.Query)
				req.Params.Arguments = newArgs
				return middleware.DatasourceProxyHandler(d, middleware.DatasourceSpec{
					Role: authz.RoleViewer, NeedTenant: obsv1alpha2.TenantTypeData, NameContains: []string{"tempo"},
					InstantPath: "api/search", QueryArg: "q", ExtraArg: "limit",
				})(ctx, req)
			default:
				return mcp.NewToolResultError(fmt.Sprintf("panel %d uses unsupported datasource kind %q (only mimir/loki/tempo)", panel.ID, kind)), nil
			}
		}),
	)

	s.AddTool(
		mcp.NewTool("generate_deeplink",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Build a ready-to-share Grafana URL for a dashboard (or a single panel in view mode) with an embedded time range and optional template-variable values. Hand the URL back to a human operator."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("uid", mcp.Required(), mcp.Description("Dashboard UID.")),
			mcp.WithNumber("panelId", mcp.Description("Open in view-panel mode if set.")),
			mcp.WithString("from", mcp.Description("Grafana time expression (e.g. 'now-1h', unix ms, RFC3339). Default 'now-1h'.")),
			mcp.WithString("to", mcp.Description("Grafana time expression. Default 'now'.")),
			mcp.WithObject("vars", mcp.Description("Template-variable values as {var: value} (e.g. {\"cluster\":\"prod-eu-1\"}).")),
		),
		middleware.Handle("generate_deeplink", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			uid := middleware.StrArg(args, "uid")
			if uid == "" {
				return mcp.NewToolResultError("missing required argument 'uid'"), nil
			}
			oa, err := d.Resolver.Require(ctx, middleware.CallerAuthz(ctx), org, authz.RoleViewer)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			base, err := d.Grafana.BaseURL()
			if err != nil {
				return mcp.NewToolResultErrorFromErr("base url", err), nil
			}
			from := middleware.StrArg(args, "from")
			if from == "" {
				from = "now-1h"
			}
			to := middleware.StrArg(args, "to")
			if to == "" {
				to = "now"
			}
			q := url.Values{}
			q.Set("orgId", strconv.FormatInt(oa.OrgID, 10))
			q.Set("from", from)
			q.Set("to", to)
			if pid := middleware.IntArg(args, "panelId"); pid > 0 {
				q.Set("viewPanel", strconv.Itoa(pid))
			}
			if vars, ok := args["vars"].(map[string]any); ok {
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
		}),
	)
}

// grafanaTimeArg returns a Grafana-friendly time string (unix ms by default).
// When the named arg is set, it's passed through verbatim (already RFC3339,
// "now-1h", or a unix-ms numeral). When absent, returns now+offset in ms.
func grafanaTimeArg(args map[string]any, name string, offset time.Duration) string {
	if s := middleware.StrArg(args, name); s != "" {
		return s
	}
	return fmt.Sprintf("%d", time.Now().Add(offset).UnixMilli())
}

// panelTarget is the subset of a Grafana panel target we need. Different
// datasources spell the query differently (Prometheus uses Expr, Tempo uses
// Query), so we accept both fields.
type panelTarget struct {
	RefID      string          `json:"refId"`
	Expr       string          `json:"expr"`
	Query      string          `json:"query"`
	Datasource json.RawMessage `json:"datasource"`
}

// pickPanelTarget walks the dashboard, finds the panel by id, picks the
// requested target (by index, default 0), and resolves which kind of
// datasource it points at (mimir/loki/tempo). Datasource resolution prefers
// the panel-level datasource ref over the target-level one.
func pickPanelTarget(raw json.RawMessage, panelID, targetIdx int) (rawPanel, panelTarget, string, map[string]string, error) {
	var doc struct {
		Dashboard struct {
			Panels     []rawPanel    `json:"panels"`
			Templating rawTemplating `json:"templating"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}
	var found *rawPanel
	var walk func(ps []rawPanel)
	walk = func(ps []rawPanel) {
		for i := range ps {
			if ps[i].Type == "row" {
				walk(ps[i].Panels)
				continue
			}
			if ps[i].ID == panelID {
				found = &ps[i]
				return
			}
		}
	}
	walk(doc.Dashboard.Panels)
	if found == nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("panel %d not found", panelID)
	}
	if targetIdx < 0 || targetIdx >= len(found.Targets) {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("panel %d: targetIndex %d out of range (len=%d)", panelID, targetIdx, len(found.Targets))
	}
	var t panelTarget
	if err := json.Unmarshal(found.Targets[targetIdx], &t); err != nil {
		return rawPanel{}, panelTarget{}, "", nil, fmt.Errorf("parse target: %w", err)
	}
	dsRaw := found.Datasource
	if len(dsRaw) == 0 || string(dsRaw) == "null" {
		dsRaw = t.Datasource
	}
	kind := datasourceKindFromRef(dsRaw, doc.Dashboard.Templating.List)
	vars := templateVarsToMap(doc.Dashboard.Templating.List)
	return *found, t, kind, vars, nil
}

// templateVarsToMap turns a dashboard's templating.list into {name: value}
// for use during panel-query variable substitution. Skips datasource
// variables (those resolve via datasourceKindFromRef instead). When a
// variable's current.value is the sentinel "$__all" or empty, we substitute
// `.+` so PromQL/LogQL regex matchers stay valid.
func templateVarsToMap(vars []rawTemplateVar) map[string]string {
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		if strings.EqualFold(v.Type, "datasource") {
			continue
		}
		raw, _ := v.Current.Value.(string)
		if raw == "" || raw == "$__all" {
			raw = ".+"
		}
		out[v.Name] = raw
	}
	return out
}

// expandGrafanaVars substitutes Grafana template macros and dashboard
// variables in `expr` so the resulting PromQL/LogQL/TraceQL is acceptable
// to Mimir/Loki/Tempo. Built-ins covered:
//
//   $__rate_interval / $__interval — defaults to step (or 5m).
//   $__interval_ms                 — step in ms (or 300000).
//   $__range / $__range_s / $__range_ms — end-start as a duration.
//
// Dashboard variables are taken from `vars` (sourced from templating.list).
// Substitution is purely textual; values are not URL-encoded.
func expandGrafanaVars(expr string, vars map[string]string, start, end, step string) string {
	intvl := step
	if intvl == "" {
		intvl = "5m"
	}
	intvlMs := durationToMillis(intvl)
	rng := computeRangeDuration(start, end)

	// Built-ins first (longer names before shorter to avoid prefix collisions).
	replacements := []struct{ from, to string }{
		{"$__rate_interval", intvl},
		{"${__rate_interval}", intvl},
		{"$__interval_ms", intvlMs},
		{"${__interval_ms}", intvlMs},
		{"$__interval", intvl},
		{"${__interval}", intvl},
		{"$__range_ms", durationToMillis(rng)},
		{"${__range_ms}", durationToMillis(rng)},
		{"$__range_s", strconv.FormatInt(durationToSeconds(rng), 10)},
		{"${__range_s}", strconv.FormatInt(durationToSeconds(rng), 10)},
		{"$__range", rng},
		{"${__range}", rng},
	}
	for _, r := range replacements {
		expr = strings.ReplaceAll(expr, r.from, r.to)
	}
	// Dashboard variables. Replace `${name}` first, then `$name` to avoid
	// `$name` matching a prefix of a longer name. We don't try to honour
	// `${name:format}` formatters — most queries use the bare form.
	for name, val := range vars {
		expr = strings.ReplaceAll(expr, "${"+name+"}", val)
		expr = strings.ReplaceAll(expr, "$"+name, val)
	}
	return expr
}

// durationToMillis returns "<n>" milliseconds for a Prometheus-shaped
// duration string (e.g. "5m" -> "300000"). Returns "300000" on parse error.
func durationToMillis(d string) string {
	td, err := time.ParseDuration(d)
	if err != nil {
		return "300000"
	}
	return strconv.FormatInt(td.Milliseconds(), 10)
}

func durationToSeconds(d string) int64 {
	td, err := time.ParseDuration(d)
	if err != nil {
		return 300
	}
	return int64(td.Seconds())
}

// computeRangeDuration turns start+end (RFC3339 or unix epoch seconds) into
// a duration string. Defaults to "1h" when start/end aren't both set.
func computeRangeDuration(start, end string) string {
	s := parseGrafanaTime(start)
	e := parseGrafanaTime(end)
	if s.IsZero() || e.IsZero() || !e.After(s) {
		return "1h"
	}
	return e.Sub(s).Round(time.Second).String()
}

func parseGrafanaTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// unix seconds (or ms — accept both by magnitude check)
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// rawTemplating is the dashboard's templating section. We read it to resolve
// datasource template variables like `$datasource` → the variable's declared
// datasource type.
type rawTemplating struct {
	List []rawTemplateVar `json:"list"`
}

type rawTemplateVar struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Query   any    `json:"query"` // for type=datasource this is the DS type string
	Current struct {
		Value any `json:"value"` // the chosen value (string for single-select; usually a string)
	} `json:"current"`
}

// datasourceKindFromRef extracts a coarse datasource kind from a Grafana
// datasource reference. Accepts three shapes + a template variable:
//
//   - object: {"type":"prometheus","uid":"..."}
//   - bare string uid that contains the type keyword
//   - "$varName" — resolved against the dashboard's templating.list: the
//     variable's `query` field carries the datasource type for type=datasource
//     variables.
//
// Returns "" if none of the above match.
func datasourceKindFromRef(raw json.RawMessage, templates []rawTemplateVar) string {
	if len(raw) == 0 {
		return ""
	}
	// String form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.HasPrefix(s, "$") || strings.HasPrefix(s, "${") {
			name := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}"), "$")
			for _, v := range templates {
				if v.Name == name && strings.EqualFold(v.Type, "datasource") {
					if qs, ok := v.Query.(string); ok {
						return kindFromTypeString(qs)
					}
				}
			}
			return ""
		}
		return kindFromTypeString(s)
	}
	// Object form: {"type":"prometheus","uid":"..."}
	var obj struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	if k := kindFromTypeString(obj.Type); k != "" {
		return k
	}
	return kindFromTypeString(obj.UID)
}

// kindFromTypeString maps a Grafana datasource type string to our internal
// kind (mimir/loki/tempo). Also handles uid substrings as a fallback.
func kindFromTypeString(s string) string {
	ls := strings.ToLower(s)
	switch {
	case ls == "prometheus" || ls == "mimir" ||
		strings.Contains(ls, "mimir") || strings.Contains(ls, "prometheus"):
		return "mimir"
	case ls == "loki" || strings.Contains(ls, "loki"):
		return "loki"
	case ls == "tempo" || strings.Contains(ls, "tempo"):
		return "tempo"
	}
	return ""
}

// readJSONPointer resolves an RFC 6901 JSON Pointer against a JSON document
// and returns the sub-tree re-serialised as JSON. The empty pointer returns
// the whole document. Arrays are indexed by decimal number; the "-" segment
// is not supported (no way to append on a read).
func readJSONPointer(doc []byte, pointer string) ([]byte, error) {
	if pointer == "" || pointer == "/" {
		return doc, nil
	}
	if pointer[0] != '/' {
		return nil, fmt.Errorf("invalid pointer %q: must start with '/'", pointer)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}
	for _, raw := range strings.Split(pointer[1:], "/") {
		// RFC 6901 escapes: ~1 = /, ~0 = ~
		tok := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[tok]
			if !ok {
				return nil, fmt.Errorf("pointer %q: key %q not found", pointer, tok)
			}
			v = next
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil {
				return nil, fmt.Errorf("pointer %q: expected array index, got %q", pointer, tok)
			}
			if idx < 0 || idx >= len(cur) {
				return nil, fmt.Errorf("pointer %q: index %d out of range (len=%d)", pointer, idx, len(cur))
			}
			v = cur[idx]
		default:
			return nil, fmt.Errorf("pointer %q: segment %q traverses non-container (%T)", pointer, tok, cur)
		}
	}
	return json.Marshal(v)
}

// groupDashboardsByFolder transforms Grafana's flat /api/search response into
// a compact folder tree. Dropping unused fields and grouping typically shrinks
// payload from O(dashboards × 400 bytes) to O(dashboards × 80 bytes).
//
// If folderFilter is non-empty, only that folder is returned (case-insensitive
// match; the sentinel "(no folder)" matches root-level dashboards).
// When the folder count exceeds pageSize, the result is sliced and a
// nextPage hint is set.
func groupDashboardsByFolder(raw json.RawMessage, folderFilter string, page, pageSize int) (any, error) {
	type item struct {
		UID         string `json:"uid"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		FolderTitle string `json:"folderTitle"`
		Type        string `json:"type"`
	}
	var items []item
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("unmarshal dashboards: %w", err)
	}

	type dashEntry struct {
		Title string `json:"title"`
		UID   string `json:"uid"`
		URL   string `json:"url,omitempty"`
	}
	const noFolder = "(no folder)"
	byFolder := map[string][]dashEntry{}
	for _, it := range items {
		if it.Type != "" && it.Type != "dash-db" {
			continue
		}
		f := it.FolderTitle
		if f == "" {
			f = noFolder
		}
		byFolder[f] = append(byFolder[f], dashEntry{Title: it.Title, UID: it.UID, URL: it.URL})
	}

	if folderFilter != "" {
		key := folderFilter
		// case-insensitive folder match
		var match string
		for k := range byFolder {
			if strings.EqualFold(k, key) {
				match = k
				break
			}
		}
		if match == "" {
			byFolder = map[string][]dashEntry{}
		} else {
			byFolder = map[string][]dashEntry{match: byFolder[match]}
		}
	}

	type folderView struct {
		Title      string      `json:"title"`
		Count      int         `json:"count"`
		Dashboards []dashEntry `json:"dashboards"`
	}
	folders := make([]folderView, 0, len(byFolder))
	total := 0
	for name, ds := range byFolder {
		sort.Slice(ds, func(i, j int) bool { return ds[i].Title < ds[j].Title })
		folders = append(folders, folderView{Title: name, Count: len(ds), Dashboards: ds})
		total += len(ds)
	}
	sort.Slice(folders, func(i, j int) bool {
		// Put "(no folder)" last so folder-organised content reads top-down.
		if folders[i].Title == noFolder {
			return false
		}
		if folders[j].Title == noFolder {
			return true
		}
		return strings.ToLower(folders[i].Title) < strings.ToLower(folders[j].Title)
	})

	if pageSize <= 0 {
		pageSize = 20
	}
	pageSize = middleware.ClampInt(pageSize, 1, 200)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(folders))
	end := min(start+pageSize, len(folders))

	return struct {
		Total         int          `json:"total"`
		TotalFolders  int          `json:"totalFolders"`
		Page          int          `json:"page"`
		PageSize      int          `json:"pageSize"`
		HasMore       bool         `json:"hasMore"`
		Folders       []folderView `json:"folders"`
	}{
		Total:        total,
		TotalFolders: len(folders),
		Page:         page,
		PageSize:     pageSize,
		HasMore:      end < len(folders),
		Folders:      folders[start:end],
	}, nil
}

// summariseDashboard projects the full dashboard JSON to a compact overview:
// metadata, template variables (with defaults), and a row/panel tree with
// titles and types but NO queries. Typical size: 1-3% of full dashboard JSON.
func summariseDashboard(raw json.RawMessage) (any, error) {
	var doc struct {
		Dashboard struct {
			UID       string `json:"uid"`
			Title     string `json:"title"`
			Tags      []string `json:"tags"`
			// Grafana permits either a string ("30s") or boolean false (disabled).
			// Use RawMessage and render as a string below.
			Refresh   json.RawMessage `json:"refresh"`
			Templating struct {
				List []struct {
					Name    string `json:"name"`
					Label   string `json:"label"`
					Type    string `json:"type"`
					Current struct {
						Value any `json:"value"`
					} `json:"current"`
				} `json:"list"`
			} `json:"templating"`
			Panels []rawPanel `json:"panels"`
		} `json:"dashboard"`
		Meta struct {
			URL       string `json:"url"`
			FolderID  int    `json:"folderId"`
			FolderURL string `json:"folderUrl"`
			Version   int    `json:"version"`
			Updated   string `json:"updated"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}

	type panelSummary struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	type rowSummary struct {
		ID     int            `json:"id,omitempty"`
		Title  string         `json:"title,omitempty"`
		Panels []panelSummary `json:"panels"`
	}
	var rows []rowSummary
	flushRow := func(r *rowSummary) {
		if r != nil && (r.Title != "" || len(r.Panels) > 0) {
			rows = append(rows, *r)
		}
	}

	// Grafana dashboards: panels live either flat (modern) or nested under
	// type:"row" panels (legacy). We handle both.
	if len(doc.Dashboard.Panels) > 0 {
		var cur *rowSummary
		for _, p := range doc.Dashboard.Panels {
			if p.Type == "row" {
				flushRow(cur)
				r := rowSummary{ID: p.ID, Title: p.Title}
				for _, nested := range p.Panels {
					r.Panels = append(r.Panels, panelSummary{ID: nested.ID, Title: nested.Title, Type: nested.Type})
				}
				cur = &r
				continue
			}
			if cur == nil {
				cur = &rowSummary{}
			}
			cur.Panels = append(cur.Panels, panelSummary{ID: p.ID, Title: p.Title, Type: p.Type})
		}
		flushRow(cur)
	}

	type tmplVar struct {
		Name    string `json:"name"`
		Label   string `json:"label,omitempty"`
		Type    string `json:"type"`
		Current any    `json:"current,omitempty"`
	}
	vars := make([]tmplVar, 0, len(doc.Dashboard.Templating.List))
	for _, v := range doc.Dashboard.Templating.List {
		vars = append(vars, tmplVar{Name: v.Name, Label: v.Label, Type: v.Type, Current: v.Current.Value})
	}

	return struct {
		UID       string     `json:"uid"`
		Title     string     `json:"title"`
		Tags      []string   `json:"tags,omitempty"`
		Refresh   string     `json:"refresh,omitempty"`
		URL       string     `json:"url,omitempty"`
		Version   int        `json:"version,omitempty"`
		Updated   string     `json:"updated,omitempty"`
		Variables []tmplVar  `json:"variables,omitempty"`
		Rows      []rowSummary `json:"rows"`
		TotalPanels int      `json:"totalPanels"`
	}{
		UID:         doc.Dashboard.UID,
		Title:       doc.Dashboard.Title,
		Tags:        doc.Dashboard.Tags,
		Refresh:     refreshToString(doc.Dashboard.Refresh),
		URL:         doc.Meta.URL,
		Version:     doc.Meta.Version,
		Updated:     doc.Meta.Updated,
		Variables:   vars,
		Rows:        rows,
		TotalPanels: countPanels(doc.Dashboard.Panels),
	}, nil
}

// refreshToString renders Grafana's polymorphic "refresh" field (string or
// bool) as a single string: "30s" stays as "30s", false becomes "" (disabled).
func refreshToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return ""
	}
	return strings.Trim(string(raw), `"`)
}

// rawPanel is the subset of the Grafana panel shape we decode. Targets are
// retained as json.RawMessage because Grafana shells out query expressions in
// different fields per datasource type (expr / query / rawSql / queryText…).
type rawPanel struct {
	ID      int               `json:"id"`
	Type    string            `json:"type"`
	Title   string            `json:"title"`
	Targets []json.RawMessage `json:"targets"`
	Panels  []rawPanel        `json:"panels"`
	Datasource json.RawMessage `json:"datasource"`
}

func countPanels(ps []rawPanel) int {
	n := 0
	for _, p := range ps {
		if p.Type == "row" {
			n += countPanels(p.Panels)
			continue
		}
		n++
	}
	return n
}

// extractDashboardQueries walks a dashboard's panels and returns the raw
// query expressions per panel. Filters by panelID (exact match) or
// titleContains (case-insensitive substring) when non-empty/positive.
func extractDashboardQueries(raw json.RawMessage, panelID int, titleContains string) (any, error) {
	var doc struct {
		Dashboard struct {
			UID    string     `json:"uid"`
			Title  string     `json:"title"`
			Panels []rawPanel `json:"panels"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard: %w", err)
	}

	type query struct {
		RefID         string `json:"refId,omitempty"`
		Expr          string `json:"expr,omitempty"`          // Prometheus
		Query         string `json:"query,omitempty"`         // many
		RawSQL        string `json:"rawSql,omitempty"`        // SQL-ish
		QueryText     string `json:"queryText,omitempty"`     // some
		Datasource    any    `json:"datasource,omitempty"`
	}
	type panelOut struct {
		ID         int     `json:"id"`
		Title      string  `json:"title"`
		Type       string  `json:"type"`
		Datasource any     `json:"datasource,omitempty"`
		Queries    []query `json:"queries"`
	}
	out := []panelOut{}
	var walk func(ps []rawPanel)
	titleLC := strings.ToLower(titleContains)
	walk = func(ps []rawPanel) {
		for _, p := range ps {
			if p.Type == "row" {
				walk(p.Panels)
				continue
			}
			if panelID > 0 && p.ID != panelID {
				continue
			}
			if titleContains != "" && !strings.Contains(strings.ToLower(p.Title), titleLC) {
				continue
			}
			po := panelOut{ID: p.ID, Title: p.Title, Type: p.Type}
			if len(p.Datasource) > 0 {
				var ds any
				_ = json.Unmarshal(p.Datasource, &ds)
				po.Datasource = ds
			}
			for _, t := range p.Targets {
				var q query
				_ = json.Unmarshal(t, &q)
				po.Queries = append(po.Queries, q)
			}
			out = append(out, po)
		}
	}
	walk(doc.Dashboard.Panels)

	return struct {
		UID    string     `json:"uid"`
		Title  string     `json:"title"`
		Count  int        `json:"count"`
		Panels []panelOut `json:"panels"`
	}{UID: doc.Dashboard.UID, Title: doc.Dashboard.Title, Count: len(out), Panels: out}, nil
}
