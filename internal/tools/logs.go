package tools

import (
	"cmp"
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
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerLogTools(s *mcpsrv.MCPServer, d *Deps) {
	// query_loki_logs — Loki range queries with cursor pagination. Loki does not
	// support instant queries for log streams, so we always call query_range
	// and default to the last hour when start/end are missing.
	s.AddTool(
		mcp.NewTool("query_loki_logs",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Run a LogQL query against Loki. Defaults to last 1h when start/end are omitted. Returns {data, nextStart} — when nextStart is set, the limit was hit; re-run with end=<nextStart> to page further back in time."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("LogQL expression")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix nanoseconds (default: now-1h)")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix nanoseconds (default: now)")),
			mcp.WithNumber("limit", mcp.Description("Max log entries per page (default 100, max 5000).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindLoki)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 30*time.Second)
			defer cancel()

			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := req.GetInt("limit", 0)
			if limit <= 0 {
				limit = 100
			}
			limit = clampInt(limit, 1, 5000)

			q := url.Values{}
			q.Set("query", query)
			start := req.GetString("start", "")
			end := req.GetString("end", "")
			if start == "" {
				start = fmt.Sprintf("%d", time.Now().Add(-time.Hour).UnixNano())
			}
			if end == "" {
				end = fmt.Sprintf("%d", time.Now().UnixNano())
			}
			q.Set("start", start)
			q.Set("end", end)
			q.Set("limit", strconv.Itoa(limit))
			q.Set("direction", "backward")

			observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/query_range").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "loki/api/v1/query_range", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("loki query_range", err), nil
			}
			cursor, hit := lokiPageCursor(body, limit)
			if capErr := enforceResponseCap(body); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			// Attach a cursor wrapper so LLM clients can page.
			return mcp.NewToolResultJSON(struct {
				NextStart string          `json:"nextStart,omitempty"`
				LimitHit  bool            `json:"limitHit"`
				Data      json.RawMessage `json:"data"`
			}{
				NextStart: cursor,
				LimitHit:  hit,
				Data:      body,
			})
		},
	)

	s.AddTool(
		mcp.NewTool("list_loki_label_names",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List log-stream label names in Loki for the given time window. Sorted alphabetically."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix ns; default now-1h.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix ns; default now.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			names, err := fetchLokiLabels(ctx, d, org, "", req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(paginateStrings(names, "", 0, len(names)))
		},
	)

	s.AddTool(
		mcp.NewTool("list_loki_label_values",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List values for a given Loki label (e.g. 'namespace', 'app'). Paginated, with optional prefix filter."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("label", mcp.Required(), mcp.Description("Label name, e.g. 'namespace'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix ns; default now-1h.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix ns; default now.")),
			mcp.WithString("prefix", mcp.Description("Case-insensitive substring filter applied after fetching.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 100, max 1000.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			label, err := req.RequireString("label")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			values, err := fetchLokiLabels(ctx, d, org, label, req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(paginateStrings(values, req.GetString("prefix", ""), req.GetInt("page", 0), req.GetInt("pageSize", 0)))
		},
	)

	s.AddTool(
		mcp.NewTool("query_loki_patterns",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Extract repeating log patterns from Loki for a LogQL selector (Loki's /loki/api/v1/patterns). Returns the top-N patterns ranked by total count, with per-pattern sample totals — the per-timestamp sample arrays are folded to a single count to keep responses compact. Requires Loki 3.x with the pattern-ingester feature."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("LogQL selector, e.g. '{namespace=\"kube-system\"}'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix ns; default now-1h.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix ns; default now.")),
			mcp.WithString("step", mcp.Description("Pattern aggregation step, e.g. '1m'. Default unset (Loki picks).")),
			mcp.WithNumber("limit", mcp.Description("Top-N patterns to return (default 50, max 500).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindLoki)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 15*time.Second)
			defer cancel()
			q := url.Values{}
			q.Set("query", query)
			q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).UnixNano())))
			q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().UnixNano())))
			if step := req.GetString("step", ""); step != "" {
				q.Set("step", step)
			}
			observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/patterns").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "loki/api/v1/patterns", q)
			if err != nil {
				// Loki versions without the pattern ingester return 404 on
				// this path; surface a readable error instead of leaking the
				// raw Grafana-wrapped 404.
				if strings.Contains(err.Error(), "status 404") {
					return mcp.NewToolResultError("loki pattern extraction not available — requires Loki 3.x with pattern-ingester enabled"), nil
				}
				return mcp.NewToolResultErrorFromErr("loki patterns", err), nil
			}
			limit := req.GetInt("limit", 0)
			if limit <= 0 {
				limit = 50
			}
			limit = clampInt(limit, 1, 500)
			projected, err := projectLokiPatterns(body, limit)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("parse patterns", err), nil
			}
			return mcp.NewToolResultJSON(projected)
		},
	)

	s.AddTool(
		mcp.NewTool("query_loki_stats",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Return Loki's size estimate (bytes, streams, chunks, entries) for a LogQL selector over a time window. Cheap way to check how much a query would return BEFORE actually running it."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("LogQL selector (must be a selector/matcher, not a metric query).")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix ns; default now-1h.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix ns; default now.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindLoki)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 10*time.Second)
			defer cancel()
			q := url.Values{}
			q.Set("query", query)
			q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).UnixNano())))
			q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().UnixNano())))
			observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/index/stats").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "loki/api/v1/index/stats", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("loki stats", err), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)
}

// fetchLokiLabels hits /loki/api/v1/labels (when label is "") or
// /loki/api/v1/label/{label}/values. Defaults the time window to last 1h
// so callers don't have to specify it for simple discovery.
func fetchLokiLabels(ctx context.Context, d *Deps, org, label string, req mcp.CallToolRequest) ([]string, error) {
	oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindLoki)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withToolTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{}
	q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).UnixNano())))
	q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().UnixNano())))

	path := "loki/api/v1/labels"
	if label != "" {
		path = "loki/api/v1/label/" + url.PathEscape(label) + "/values"
	}
	observability.GrafanaProxyTotal.WithLabelValues(path).Inc()
	body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, path, q)
	if err != nil {
		return nil, fmt.Errorf("loki %s: %w", path, err)
	}
	var env struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("unmarshal loki response: %w", err)
	}
	return env.Data, nil
}

// projectLokiPatterns folds Loki's /api/v1/patterns response down to a
// compact top-N view. Loki returns
//
//	{"status":"success","data":[{"pattern":"...","samples":[[ts,count],...]}]}
//
// Per-timestamp sample arrays dominate the payload and aren't useful for an
// LLM asked "what's spamming this stream?". We sum per pattern, sort desc,
// and drop the raw series.
func projectLokiPatterns(raw json.RawMessage, limit int) (any, error) {
	var env struct {
		Status string `json:"status"`
		Data   []struct {
			Pattern string   `json:"pattern"`
			Samples [][2]any `json:"samples"` // [ts, count] pairs; ts+count are JSON numbers
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("unmarshal patterns: %w", err)
	}
	type item struct {
		Pattern string `json:"pattern"`
		Count   int64  `json:"count"`
	}
	items := make([]item, 0, len(env.Data))
	for _, p := range env.Data {
		var total int64
		for _, s := range p.Samples {
			if len(s) < 2 {
				continue
			}
			switch v := s[1].(type) {
			case float64:
				total += int64(v)
			case json.Number:
				if i, err := v.Int64(); err == nil {
					total += i
				}
			}
		}
		items = append(items, item{Pattern: p.Pattern, Count: total})
	}
	// Sort desc by count; break ties by shorter pattern first (stable-ish).
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return len(items[i].Pattern) < len(items[j].Pattern)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return struct {
		Total    int    `json:"total"`
		Returned int    `json:"returned"`
		Items    []item `json:"items"`
	}{Total: len(env.Data), Returned: len(items), Items: items}, nil
}

// lokiPageCursor walks the query_range result to find the oldest entry's
// timestamp. When the result set size matches or exceeds limit, that
// timestamp is the natural "end" to use for the next (older) page.
// Returns ("", false) when the limit was not hit, meaning no more pages.
func lokiPageCursor(body []byte, limit int) (nextStart string, limitHit bool) {
	var env struct {
		Data struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", false
	}
	total := 0
	oldest := ""
	for _, r := range env.Data.Result {
		total += len(r.Values)
		for _, v := range r.Values {
			ts := v[0]
			if oldest == "" || ts < oldest {
				oldest = ts
			}
		}
	}
	if total < limit {
		return "", false
	}
	return oldest, true
}
