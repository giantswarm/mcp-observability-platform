package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/middleware"
)

func registerMetricsTools(s *mcpsrv.MCPServer, d *middleware.Deps) {
	registerSingleAlertRuleTool(s, d)

	// query_metrics — Mimir via Grafana datasource proxy.
	s.AddTool(
		mcp.NewTool("query_metrics",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Run a PromQL query against Mimir via the org's multi-tenant datasource. Runs /api/v1/query_range when both start and end are set, otherwise /api/v1/query. Prefer aggregations (sum by / rate / topk) over raw series."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("PromQL expression")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch; if set with 'end', runs query_range")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch")),
			mcp.WithString("step", mcp.Description("Step for query_range, e.g. 30s, 1m")),
		),
		middleware.Handle("query_metrics", d, middleware.DatasourceProxyHandler(d, middleware.DatasourceSpec{
			Role:          authz.RoleViewer,
			NeedTenant:    obsv1alpha2.TenantTypeData,
			NameContains:  []string{"mimir"},
			InstantPath:   "api/v1/query",
			RangePath:     "api/v1/query_range",
			QueryArg:      "query",
			SupportsRange: true,
			Timeout:       30 * time.Second,
		})),
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_metric_names",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("List metric names available in Mimir. Use prefix to narrow by substring; use match[] to narrow by a PromQL selector (e.g. '{job=\"api\"}'). Paginated."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("prefix", mcp.Description("Case-insensitive substring filter applied after fetching.")),
			mcp.WithString("match", mcp.Description("Optional PromQL selector forwarded to /label/__name__/values as match[].")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch seconds; narrows the time window considered.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 100, max 1000.")),
		),
		middleware.Handle("list_prometheus_metric_names", d, metricLabelValuesHandler(d, "__name__")),
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_label_names",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("List label names for Mimir series matching an optional selector. Sorted alphabetically."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("match", mcp.Description("Optional PromQL selector, e.g. '{job=\"api\"}'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch seconds.")),
		),
		middleware.Handle("list_prometheus_label_names", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, dsID, err := middleware.ResolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, "mimir")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := promSelectorArgs(args)
			names, err := fetchPromLabelList(ctx, d, oa.OrgID, dsID, "api/v1/labels", q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			sort.Strings(names)
			return mcp.NewToolResultJSON(struct {
				Total int      `json:"total"`
				Items []string `json:"items"`
			}{Total: len(names), Items: names})
		}),
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_label_values",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("List values for a given label from Mimir, optionally narrowed by a PromQL selector. Paginated."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("label", mcp.Required(), mcp.Description("Label name, e.g. 'cluster' or 'job'.")),
			mcp.WithString("match", mcp.Description("Optional PromQL selector.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithString("prefix", mcp.Description("Case-insensitive substring filter.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 100, max 1000.")),
		),
		middleware.Handle("list_prometheus_label_values", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			label := middleware.StrArg(args, "label")
			if label == "" {
				return mcp.NewToolResultError("missing required argument 'label'"), nil
			}
			return runPromLabelValues(ctx, d, org, label, args)
		}),
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_metric_metadata",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Return Prometheus metric metadata (HELP, TYPE) from Mimir. Useful to understand what a metric measures before building a PromQL expression."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("metric", mcp.Description("Metric name. If empty, all metadata is returned (may be large).")),
			mcp.WithNumber("limit", mcp.Description("Cap on the number of metric families returned (default 200).")),
		),
		middleware.Handle("list_prometheus_metric_metadata", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, dsID, err := middleware.ResolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, "mimir")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := url.Values{}
			if m := middleware.StrArg(args, "metric"); m != "" {
				q.Set("metric", m)
			}
			if lim := middleware.IntArg(args, "limit"); lim > 0 {
				q.Set("limit", fmt.Sprintf("%d", lim))
			}
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/metadata").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), dsID, "api/v1/metadata", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("mimir metadata", err), nil
			}
			if capErr := middleware.EnforceResponseCap(body); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			return mcp.NewToolResultText(string(body)), nil
		}),
	)

	s.AddTool(
		mcp.NewTool("query_prometheus_histogram",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Convenience wrapper that computes a percentile over a Prometheus histogram metric. Builds and runs `histogram_quantile(q, sum by (le, ...) (rate(<metric>{<matchers>}[<window>])))` against Mimir. Use this instead of crafting the expression by hand when you know the metric is a `_bucket` histogram."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("metric", mcp.Required(), mcp.Description("The `_bucket` metric name, e.g. 'http_request_duration_seconds_bucket'.")),
			mcp.WithNumber("quantile", mcp.Description("Target quantile in [0,1]. Default 0.95 (p95).")),
			mcp.WithString("window", mcp.Description("Rate window, e.g. '5m'. Default '5m'.")),
			mcp.WithString("matchers", mcp.Description("Optional inner selector, e.g. 'job=\"api\",cluster_id=\"graveler\"'. Do NOT include surrounding braces.")),
			mcp.WithString("groupBy", mcp.Description("Comma-separated labels to preserve alongside 'le', e.g. 'cluster_id,route'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch; if set with 'end', runs query_range.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch.")),
			mcp.WithString("step", mcp.Description("Step for query_range, e.g. '30s', '1m'.")),
		),
		middleware.Handle("query_prometheus_histogram", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			metric := middleware.StrArg(args, "metric")
			if metric == "" {
				return mcp.NewToolResultError("missing required argument 'metric'"), nil
			}
			// Validate metric name (basic safety — PromQL metric names are
			// [a-zA-Z_:][a-zA-Z0-9_:]*). This prevents a caller from
			// injecting arbitrary PromQL via the metric argument.
			if !isValidPromIdent(metric) {
				return mcp.NewToolResultError("invalid metric name; must match [a-zA-Z_:][a-zA-Z0-9_:]*"), nil
			}
			q := histogramQuantileArg(args)
			window := firstNonEmptyStr(middleware.StrArg(args, "window"), "5m")
			matchers := middleware.StrArg(args, "matchers")
			groupBy := middleware.StrArg(args, "groupBy")

			expr := buildHistogramQuantile(q, metric, matchers, window, groupBy)
			// Forward to the same proxy path as query_metrics.
			newArgs := map[string]any{
				"org":   org,
				"query": expr,
			}
			for _, k := range []string{"start", "end", "step"} {
				if v := middleware.StrArg(args, k); v != "" {
					newArgs[k] = v
				}
			}
			// Re-use datasourceProxyHandler by synthesising a tool request.
			req.Params.Arguments = newArgs
			return middleware.DatasourceProxyHandler(d, middleware.DatasourceSpec{
				Role:          authz.RoleViewer,
				NeedTenant:    obsv1alpha2.TenantTypeData,
				NameContains:  []string{"mimir"},
				InstantPath:   "api/v1/query",
				RangePath:     "api/v1/query_range",
				QueryArg:      "query",
				SupportsRange: true,
				Timeout:       30 * time.Second,
			})(ctx, req)
		}),
	)

	s.AddTool(
		mcp.NewTool("list_alert_rules",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("List Prometheus recording & alerting rules loaded by Mimir (distinct from firing Alertmanager alerts). Filter by type, state, or name substring."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("type", mcp.Description("'alert' | 'record' | 'all' (default 'alert').")),
			mcp.WithString("state", mcp.Description("For alerting rules: 'firing' | 'pending' | 'inactive' | 'all' (default 'all').")),
			mcp.WithString("nameContains", mcp.Description("Case-insensitive name substring filter.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 50, max 500.")),
		),
		middleware.Handle("list_alert_rules", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			oa, dsID, err := middleware.ResolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, "mimir")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
			defer cancel()
			ruleType := strings.ToLower(middleware.StrArg(args, "type"))
			if ruleType == "" {
				ruleType = "alert"
			}
			wantState := strings.ToLower(middleware.StrArg(args, "state"))
			if wantState == "" {
				wantState = "all"
			}
			nameLC := strings.ToLower(middleware.StrArg(args, "nameContains"))
			page := middleware.IntArg(args, "page")
			pageSize := middleware.IntArg(args, "pageSize")
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = middleware.ClampInt(pageSize, 1, 500)

			q := url.Values{}
			if ruleType == "alert" || ruleType == "record" {
				q.Set("type", ruleType)
			}
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/rules").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), dsID, "api/v1/rules", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("mimir rules", err), nil
			}
			rules, err := flattenAlertRules(body, ruleType, wantState, nameLC)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse rules", err), nil
			}
			start := min(page*pageSize, len(rules))
			end := min(start+pageSize, len(rules))
			return mcp.NewToolResultJSON(struct {
				Total    int        `json:"total"`
				Page     int        `json:"page"`
				PageSize int        `json:"pageSize"`
				HasMore  bool       `json:"hasMore"`
				Items    []ruleItem `json:"items"`
			}{
				Total:    len(rules),
				Page:     page,
				PageSize: pageSize,
				HasMore:  end < len(rules),
				Items:    rules[start:end],
			})
		}),
	)
}

// histogramQuantileArg extracts the quantile arg with a 0.95 default, and
// clamps it into [0,1] to avoid degenerate queries.
func histogramQuantileArg(args map[string]any) float64 {
	q := 0.95
	switch v := args["quantile"].(type) {
	case float64:
		q = v
	case int:
		q = float64(v)
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	return q
}

// buildHistogramQuantile composes the PromQL expression. `le` is always
// included in the aggregation; extra groupBy labels are appended.
func buildHistogramQuantile(q float64, metric, matchers, window, groupBy string) string {
	inner := metric
	if matchers != "" {
		inner = fmt.Sprintf("%s{%s}", metric, matchers)
	}
	by := "le"
	if groupBy != "" {
		by = "le, " + groupBy
	}
	return fmt.Sprintf("histogram_quantile(%g, sum by (%s) (rate(%s[%s])))", q, by, inner, window)
}

// isValidPromIdent reports whether s is a valid Prometheus metric-name
// identifier. Prevents injection through the `metric` tool argument.
func isValidPromIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_' || r == ':':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func registerSingleAlertRuleTool(s *mcpsrv.MCPServer, d *middleware.Deps) {
	s.AddTool(
		mcp.NewTool("get_alert_rule",
			middleware.ReadOnlyAnnotation(),
			mcp.WithDescription("Return a single Prometheus rule (alerting or recording) by name and optional group. Use after list_alert_rules when you need the full expression + labels of one specific rule."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Exact rule name.")),
			mcp.WithString("group", mcp.Description("Optional group name to disambiguate when several rules share a name.")),
		),
		middleware.Handle("get_alert_rule", d, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			org, errRes := middleware.RequireOrg(args)
			if errRes != nil {
				return errRes, nil
			}
			name := middleware.StrArg(args, "name")
			if name == "" {
				return mcp.NewToolResultError("missing required argument 'name'"), nil
			}
			group := middleware.StrArg(args, "group")
			oa, dsID, err := middleware.ResolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, "mimir")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
			defer cancel()
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/rules").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, middleware.GrafanaOpts(ctx, oa.OrgID), dsID, "api/v1/rules", url.Values{})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("mimir rules", err), nil
			}
			matches, err := flattenAlertRules(body, "all", "all", strings.ToLower(name))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse rules", err), nil
			}
			// Exact-name match (flatten uses substring); optional group filter.
			out := matches[:0]
			for _, r := range matches {
				if !strings.EqualFold(r.Name, name) {
					continue
				}
				if group != "" && !strings.EqualFold(r.Group, group) {
					continue
				}
				out = append(out, r)
			}
			if len(out) == 0 {
				return mcp.NewToolResultError(fmt.Sprintf("rule %q not found in org %q", name, org)), nil
			}
			return mcp.NewToolResultJSON(struct {
				Rules []ruleItem `json:"rules"`
			}{Rules: out})
		}),
	)
}

// metricLabelValuesHandler wraps the shared handler that resolves Mimir and
// runs /api/v1/label/{label}/values with optional match[] narrowing.
func metricLabelValuesHandler(d *middleware.Deps, label string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		org, errRes := middleware.RequireOrg(args)
		if errRes != nil {
			return errRes, nil
		}
		args["label"] = label
		return runPromLabelValues(ctx, d, org, label, args)
	}
}

// runPromLabelValues is the shared core of the metric-names and
// label-values tools: call /api/v1/label/{label}/values with match[] +
// time filters, then apply client-side prefix filter + pagination.
func runPromLabelValues(ctx context.Context, d *middleware.Deps, org, label string, args map[string]any) (*mcp.CallToolResult, error) {
	oa, dsID, err := middleware.ResolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, "mimir")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ctx, cancel := middleware.WithToolTimeout(ctx, 15*time.Second)
	defer cancel()
	q := promSelectorArgs(args)
	path := "api/v1/label/" + url.PathEscape(label) + "/values"
	names, err := fetchPromLabelList(ctx, d, oa.OrgID, dsID, path, q)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(middleware.PaginateStrings(names, middleware.StrArg(args, "prefix"), middleware.IntArg(args, "page"), middleware.IntArg(args, "pageSize")))
}

// promSelectorArgs collects optional match[] / start / end args into a
// url.Values suitable for Prometheus/Mimir label-discovery endpoints.
func promSelectorArgs(args map[string]any) url.Values {
	q := url.Values{}
	if m := middleware.StrArg(args, "match"); m != "" {
		q.Set("match[]", m)
	}
	if s := middleware.StrArg(args, "start"); s != "" {
		q.Set("start", s)
	}
	if e := middleware.StrArg(args, "end"); e != "" {
		q.Set("end", e)
	}
	return q
}

// fetchPromLabelList hits a Prometheus label-list endpoint (labels or values)
// and returns the data[] array.
func fetchPromLabelList(ctx context.Context, d *middleware.Deps, orgID, dsID int64, path string, q url.Values) ([]string, error) {
	observability.GrafanaProxyTotal.WithLabelValues(path).Inc()
	body, err := d.Grafana.DatasourceProxy(ctx, middleware.GrafanaOpts(ctx, orgID), dsID, path, q)
	if err != nil {
		return nil, fmt.Errorf("prometheus %s: %w", path, err)
	}
	var env struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("unmarshal prometheus response: %w", err)
	}
	return env.Data, nil
}

// ruleItem is the projected view of a Prometheus rule — just the fields a
// triage session needs.
type ruleItem struct {
	Type        string            `json:"type"` // alert | record
	Name        string            `json:"name"`
	Expr        string            `json:"expr"`
	State       string            `json:"state,omitempty"`
	Group       string            `json:"group"`
	File        string            `json:"file,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Health      string            `json:"health,omitempty"`
}

// flattenAlertRules walks Prometheus's nested /api/v1/rules response and
// filters in one pass. The native response shape is:
//
//	{status, data: {groups: [{name, file, rules: [{...}]}]}}
func flattenAlertRules(raw json.RawMessage, wantType, wantState, nameLC string) ([]ruleItem, error) {
	var env struct {
		Data struct {
			Groups []struct {
				Name  string `json:"name"`
				File  string `json:"file"`
				Rules []struct {
					Type        string            `json:"type"`
					Name        string            `json:"name"`
					Query       string            `json:"query"`
					State       string            `json:"state"`
					Health      string            `json:"health"`
					Labels      map[string]string `json:"labels"`
					Annotations map[string]string `json:"annotations"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	out := []ruleItem{}
	for _, g := range env.Data.Groups {
		for _, r := range g.Rules {
			if wantType == "alert" && r.Type != "alerting" {
				continue
			}
			if wantType == "record" && r.Type != "recording" {
				continue
			}
			if wantState != "all" && !strings.EqualFold(r.State, wantState) {
				continue
			}
			if nameLC != "" && !strings.Contains(strings.ToLower(r.Name), nameLC) {
				continue
			}
			ruleType := "alert"
			if r.Type == "recording" {
				ruleType = "record"
			}
			out = append(out, ruleItem{
				Type:        ruleType,
				Name:        r.Name,
				Expr:        r.Query,
				State:       r.State,
				Group:       g.Name,
				File:        g.File,
				Labels:      r.Labels,
				Annotations: r.Annotations,
				Health:      r.Health,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}
