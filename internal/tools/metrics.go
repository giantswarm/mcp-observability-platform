// Package tools — metrics.go: Mimir PromQL tools (query_prometheus, labels, metadata, histograms, alert rules).
package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerMetricsTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	registerSingleAlertRuleTool(s, az, gc)

	// query_prometheus — Mimir via Grafana datasource proxy.
	s.AddTool(
		mcp.NewTool("query_prometheus",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Run a PromQL query against Mimir via the org's multi-tenant datasource. Runs /api/v1/query_range when both start and end are set, otherwise /api/v1/query. Prefer aggregations (sum by / rate / topk) over raw series."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — either the Grafana displayName or the CR name. See list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("PromQL expression")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch; if set with 'end', runs query_range")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch")),
			mcp.WithString("step", mcp.Description("Step for query_range, e.g. 30s, 1m")),
		),
		datasourceProxyHandler(az, gc, datasourceSpec{
			Role:          authz.RoleViewer,
			NeedTenant:    authz.TenantTypeData,
			NameContains:  []string{dsKindMimir},
			InstantPath:   "api/v1/query",
			RangePath:     "api/v1/query_range",
			QueryArg:      "query",
			SupportsRange: true,
			Timeout:       30 * time.Second,
		}),
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_metric_names",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List metric names available in Mimir. Use prefix to narrow by substring; use match[] to narrow by a PromQL selector (e.g. '{job=\"api\"}'). Paginated."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("prefix", mcp.Description("Case-insensitive substring filter applied after fetching.")),
			mcp.WithString("match", mcp.Description("Optional PromQL selector forwarded to /label/__name__/values as match[].")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch seconds; narrows the time window considered.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 100, max 1000.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return runPromLabelValues(ctx, az, gc, orgRef, "__name__", req)
		},
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_label_names",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List label names for Mimir series matching an optional selector. Sorted alphabetically."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("match", mcp.Description("Optional PromQL selector, e.g. '{job=\"api\"}'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix epoch seconds.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix epoch seconds.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindMimir)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := promSelectorArgs(req)
			names, err := fetchPromLabelList(ctx, gc, org.OrgID, dsID, "api/v1/labels", "api/v1/labels", q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			sort.Strings(names)
			return mcp.NewToolResultJSON(struct {
				Total int      `json:"total"`
				Items []string `json:"items"`
			}{Total: len(names), Items: names})
		},
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_label_values",
			ReadOnlyAnnotation(),
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
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			label, err := req.RequireString("label")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return runPromLabelValues(ctx, az, gc, orgRef, label, req)
		},
	)

	s.AddTool(
		mcp.NewTool("list_prometheus_metric_metadata",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return Prometheus metric metadata (HELP, TYPE) from Mimir. Useful to understand what a metric measures before building a PromQL expression."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("metric", mcp.Description("Metric name. If empty, all metadata is returned (may be large).")),
			mcp.WithNumber("limit", mcp.Description("Cap on the number of metric families returned (default 200).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindMimir)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := url.Values{}
			if m := req.GetString("metric", ""); m != "" {
				q.Set("metric", m)
			}
			if lim := req.GetInt("limit", 0); lim > 0 {
				q.Set("limit", fmt.Sprintf("%d", lim))
			}
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/metadata").Inc()
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/v1/metadata", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("mimir metadata", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("query_prometheus_histogram",
			ReadOnlyAnnotation(),
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
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			metric, err := req.RequireString("metric")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Validate metric name (basic safety — PromQL metric names are
			// [a-zA-Z_:][a-zA-Z0-9_:]*). This prevents a caller from
			// injecting arbitrary PromQL via the metric argument.
			if !isValidPromIdent(metric) {
				return mcp.NewToolResultError("invalid metric name; must match [a-zA-Z_:][a-zA-Z0-9_:]*"), nil
			}
			q := histogramQuantileArg(req)
			window := cmp.Or(req.GetString("window", ""), "5m")
			matchers := req.GetString("matchers", "")
			groupBy := req.GetString("groupBy", "")

			expr := buildHistogramQuantile(q, metric, matchers, window, groupBy)
			// Re-dispatch to the same proxy path as query_prometheus with the
			// synthesised expression. Built as an explicit invocation rather
			// than mutating req.Params.Arguments so the audit record captures
			// the caller's actual args (metric/q/window/matchers), not the
			// internal PromQL we generated.
			return runDatasourceProxy(ctx, az, gc, datasourceSpec{
				Role:          authz.RoleViewer,
				NeedTenant:    authz.TenantTypeData,
				NameContains:  []string{dsKindMimir},
				InstantPath:   "api/v1/query",
				RangePath:     "api/v1/query_range",
				QueryArg:      "query",
				SupportsRange: true,
				Timeout:       30 * time.Second,
			}, datasourceInvocation{
				Org:   orgRef,
				Query: expr,
				Start: req.GetString("start", ""),
				End:   req.GetString("end", ""),
				Step:  req.GetString("step", ""),
			})
		},
	)

	s.AddTool(
		mcp.NewTool("list_alert_rules",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List Prometheus recording & alerting rules loaded by Mimir (distinct from firing Alertmanager alerts). Filter by type, state, or name substring."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("type", mcp.Description("'alert' | 'record' | 'all' (default 'alert').")),
			mcp.WithString("state", mcp.Description("For alerting rules: 'firing' | 'pending' | 'inactive' | 'all' (default 'all').")),
			mcp.WithString("nameContains", mcp.Description("Case-insensitive name substring filter.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 50, max 500.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindMimir)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			ruleType := strings.ToLower(req.GetString("type", ""))
			if ruleType == "" {
				ruleType = ruleTypeAlert
			}
			wantState := strings.ToLower(req.GetString("state", ""))
			if wantState == "" {
				wantState = filterAll
			}
			nameLC := strings.ToLower(req.GetString("nameContains", ""))
			page := req.GetInt("page", 0)
			pageSize := req.GetInt("pageSize", 0)
			if pageSize <= 0 {
				pageSize = 50
			}
			pageSize = clampInt(pageSize, 1, 500)

			q := url.Values{}
			if ruleType == ruleTypeAlert || ruleType == ruleTypeRecord {
				q.Set("type", ruleType)
			}
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/rules").Inc()
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/v1/rules", q)
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
		},
	)
}

// histogramQuantileArg extracts the quantile arg with a 0.95 default, and
// clamps it into [0,1] to avoid degenerate queries.
func histogramQuantileArg(req mcp.CallToolRequest) float64 {
	q := req.GetFloat("quantile", 0.95)
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

func registerSingleAlertRuleTool(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("get_alert_rule",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return a single Prometheus rule (alerting or recording) by name and optional group. Use after list_alert_rules when you need the full expression + labels of one specific rule."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Exact rule name.")),
			mcp.WithString("group", mcp.Description("Optional group name to disambiguate when several rules share a name.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			group := req.GetString("group", "")
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindMimir)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			observability.GrafanaProxyTotal.WithLabelValues("api/v1/rules").Inc()
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/v1/rules", url.Values{})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("mimir rules", err), nil
			}
			matches, err := flattenAlertRules(body, filterAll, filterAll, strings.ToLower(name))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse rules", err), nil
			}
			// Exact-name match (flatten uses substring); optional group filter.
			// Allocate a fresh slice rather than `matches[:0]` — the latter
			// aliases the caller-visible slice and is a foot-gun if flattenAlertRules
			// ever returns a cached slice.
			out := make([]ruleItem, 0, len(matches))
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
				return mcp.NewToolResultError(fmt.Sprintf("rule %q not found in org %q", name, orgRef)), nil
			}
			return mcp.NewToolResultJSON(struct {
				Rules []ruleItem `json:"rules"`
			}{Rules: out})
		},
	)
}

// runPromLabelValues is the shared core of the metric-names and
// label-values tools: call /api/v1/label/{label}/values with match[] +
// time filters, then apply client-side prefix filter + pagination.
func runPromLabelValues(ctx context.Context, az authz.Authorizer, gc grafana.Client, orgRef, label string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindMimir)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ctx, cancel := withToolTimeout(ctx, 15*time.Second)
	defer cancel()
	q := promSelectorArgs(req)
	// Use a templated metric path (":name" placeholder) so Prom label
	// cardinality stays bounded — the user-controlled label value lives
	// only in the URL, never in the metric's "path" label.
	path := "api/v1/label/" + url.PathEscape(label) + "/values"
	names, err := fetchPromLabelList(ctx, gc, org.OrgID, dsID, "api/v1/label/:name/values", path, q)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(paginateStrings(names, req.GetString("prefix", ""), req.GetInt("page", 0), req.GetInt("pageSize", 0)))
}

// promSelectorArgs collects optional match[] / start / end args into a
// url.Values suitable for Prometheus/Mimir label-discovery endpoints.
func promSelectorArgs(req mcp.CallToolRequest) url.Values {
	q := url.Values{}
	if m := req.GetString("match", ""); m != "" {
		q.Set("match[]", m)
	}
	if s := req.GetString("start", ""); s != "" {
		q.Set("start", s)
	}
	if e := req.GetString("end", ""); e != "" {
		q.Set("end", e)
	}
	return q
}

// fetchPromLabelList hits a Prometheus label-list endpoint (labels or values)
// and returns the data[] array. metricPath is the bounded-cardinality label
// used on the proxy-count metric (e.g. "api/v1/label/:name/values"); path is
// the actual URL path sent to Grafana.
func fetchPromLabelList(ctx context.Context, gc grafana.Client, orgID, dsID int64, metricPath, path string, q url.Values) ([]string, error) {
	observability.GrafanaProxyTotal.WithLabelValues(metricPath).Inc()
	body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, orgID), dsID, path, q)
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
			if wantType == ruleTypeAlert && r.Type != "alerting" {
				continue
			}
			if wantType == ruleTypeRecord && r.Type != "recording" {
				continue
			}
			if wantState != filterAll && !strings.EqualFold(r.State, wantState) {
				continue
			}
			if nameLC != "" && !strings.Contains(strings.ToLower(r.Name), nameLC) {
				continue
			}
			ruleType := ruleTypeAlert
			if r.Type == "recording" {
				ruleType = ruleTypeRecord
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
