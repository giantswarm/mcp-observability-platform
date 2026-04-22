package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

func registerTraceTools(s *mcpsrv.MCPServer, d *Deps) {
	// query_traces — Tempo TraceQL search.
	s.AddTool(
		mcp.NewTool("query_traces",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Search traces in Tempo via the org's multi-tenant datasource. Use TraceQL expressions like '{ .service.name = \"api\" && duration > 2s }'."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("TraceQL expression")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix seconds")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix seconds")),
			mcp.WithNumber("limit", mcp.Description("Max traces (default 20)")),
		),
		datasourceProxyHandler(d, datasourceSpec{
			Role:         authz.RoleViewer,
			NeedTenant:   obsv1alpha2.TenantTypeData,
			NameContains: []string{dsKindTempo},
			InstantPath:  "api/search",
			QueryArg:     "q",
			ExtraArg:     "limit",
			Timeout:      20 * time.Second,
		}),
	)

	s.AddTool(
		mcp.NewTool("query_tempo_metrics",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Run a TraceQL metrics query against Tempo (via the /api/metrics/query_range endpoint). Returns trace-derived RED metrics as a Prometheus-shaped result."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("TraceQL metrics expression, e.g. '{ .service.name = \"api\" } | rate()'.")),
			mcp.WithString("start", mcp.Description("RFC3339 or unix seconds; default now-1h.")),
			mcp.WithString("end", mcp.Description("RFC3339 or unix seconds; default now.")),
			mcp.WithString("step", mcp.Description("Step, e.g. 30s, 1m.")),
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
			oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindTempo)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ctx, cancel := withToolTimeout(ctx, 20*time.Second)
			defer cancel()
			q := url.Values{}
			q.Set("q", query)
			q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())))
			q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().Unix())))
			if step := req.GetString("step", ""); step != "" {
				q.Set("step", step)
			}
			observability.GrafanaProxyTotal.WithLabelValues("api/metrics/query_range").Inc()
			body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "api/metrics/query_range", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("tempo metrics", err), nil
			}
			if capErr := enforceResponseCap(body); capErr != nil {
				return mcp.NewToolResultJSON(capErr)
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_tempo_tag_names",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List searchable tag names in Tempo for the given time window (e.g. 'service.name', 'http.method'). Sorted alphabetically."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("scope", mcp.Description("Tempo tag scope: 'resource' | 'span' | 'intrinsic' | 'all' (default 'all').")),
			mcp.WithString("start", mcp.Description("Unix seconds; default now-1h.")),
			mcp.WithString("end", mcp.Description("Unix seconds; default now.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			names, err := fetchTempoTags(ctx, d, org, "", req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return resultJSONWithCap(paginateStrings(names, "", 0, len(names)))
		},
	)

	s.AddTool(
		mcp.NewTool("list_tempo_tag_values",
			ReadOnlyAnnotation(),
			mcp.WithDescription("List values for a given Tempo tag (e.g. values for 'service.name') in the time window. Paginated with optional prefix filter."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("tag", mcp.Required(), mcp.Description("Tag name, e.g. 'service.name'.")),
			mcp.WithString("start", mcp.Description("Unix seconds; default now-1h.")),
			mcp.WithString("end", mcp.Description("Unix seconds; default now.")),
			mcp.WithString("prefix", mcp.Description("Case-insensitive substring filter applied after fetching.")),
			mcp.WithNumber("page", mcp.Description("0-based page (default 0).")),
			mcp.WithNumber("pageSize", mcp.Description("Default 100, max 1000.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			org, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tag, err := req.RequireString("tag")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			values, err := fetchTempoTags(ctx, d, org, tag, req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return resultJSONWithCap(paginateStrings(values, req.GetString("prefix", ""), req.GetInt("page", 0), req.GetInt("pageSize", 0)))
		},
	)
}

// qualifyTempoTag prepends the scope to a Tempo tag so list_tempo_tag_values
// can round-trip the result back to Tempo's /api/v2/search/tag/{name}/values
// endpoint (which requires scope-qualified names). Intrinsic tags already
// carry the colon delimiter Tempo uses for them (e.g. "span:duration").
func qualifyTempoTag(scope, tag string) string {
	if tag == "" {
		return tag
	}
	// Intrinsic/event/link tags use "scope:field" shape from the server.
	if strings.Contains(tag, ":") {
		return tag
	}
	switch strings.ToLower(scope) {
	case "", "intrinsic":
		// Intrinsic shorthand: ".duration", ".name" — but the v2 endpoint
		// also accepts the bare name for intrinsics. Prefer the bare form.
		return tag
	case "resource", "span", "event", "link":
		return scope + "." + tag
	default:
		return scope + "." + tag
	}
}

// fetchTempoTags hits /api/v2/search/tags (when tag is "") or
// /api/v2/search/tag/{tag}/values. Tempo's v2 API returns a single-level
// {scopes:[{name, tags:[...]}]} structure for tag names and
// {tagValues:[{type, value}]} for values; we flatten both to a []string.
func fetchTempoTags(ctx context.Context, d *Deps, org, tag string, req mcp.CallToolRequest) ([]string, error) {
	oa, dsID, err := resolveDatasource(ctx, d, org, authz.RoleViewer, obsv1alpha2.TenantTypeData, dsKindTempo)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withToolTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{}
	q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())))
	q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().Unix())))
	if scope := req.GetString("scope", ""); scope != "" && scope != filterAll {
		q.Set("scope", scope)
	}

	if tag == "" {
		observability.GrafanaProxyTotal.WithLabelValues("api/v2/search/tags").Inc()
		body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, "api/v2/search/tags", q)
		if err != nil {
			return nil, fmt.Errorf("tempo tags: %w", err)
		}
		var v2 struct {
			Scopes []struct {
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			} `json:"scopes"`
		}
		if err := json.Unmarshal(body, &v2); err != nil {
			return nil, fmt.Errorf("unmarshal tempo tags: %w", err)
		}
		if len(v2.Scopes) > 0 {
			// Tempo's v2 tag-values endpoint requires scope-qualified names
			// (e.g. "resource.service.name", "span.http.method"), except for
			// intrinsics which use ".name" / ".duration" shorthand. Preserve
			// that scoping here so list_tempo_tag_values receives a usable
			// identifier round-trip.
			var out []string
			for _, s := range v2.Scopes {
				for _, t := range s.Tags {
					out = append(out, qualifyTempoTag(s.Name, t))
				}
			}
			return out, nil
		}
		// Fallback to v1 shape {tagNames: []}
		var v1 struct {
			TagNames []string `json:"tagNames"`
		}
		if err := json.Unmarshal(body, &v1); err != nil {
			return nil, fmt.Errorf("unmarshal tempo tags v1: %w", err)
		}
		return v1.TagNames, nil
	}

	path := "api/v2/search/tag/" + url.PathEscape(tag) + "/values"
	observability.GrafanaProxyTotal.WithLabelValues(path).Inc()
	body, err := d.Grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, path, q)
	if err != nil {
		return nil, fmt.Errorf("tempo tag values: %w", err)
	}
	var v2 struct {
		TagValues []struct {
			Value string `json:"value"`
		} `json:"tagValues"`
	}
	if err := json.Unmarshal(body, &v2); err != nil {
		return nil, fmt.Errorf("unmarshal tempo tag values: %w", err)
	}
	if len(v2.TagValues) > 0 {
		out := make([]string, 0, len(v2.TagValues))
		for _, tv := range v2.TagValues {
			out = append(out, tv.Value)
		}
		return out, nil
	}
	// v1 fallback: {tagValues:[string]}
	var v1 struct {
		TagValues []string `json:"tagValues"`
	}
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, fmt.Errorf("unmarshal tempo tag values v1: %w", err)
	}
	return v1.TagValues, nil
}
