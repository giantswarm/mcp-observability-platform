// Package tools — traces.go: Tempo trace tools (query_traces + tag
// discovery). Local because upstream grafana/mcp-grafana has no Tempo
// surface today. Tempo's own MCP server plus mcp-grafana's proxy
// (proxied_tools.go, NewToolManager) is the path forward — see
// roadmap. Local until per-tenant mcp_server.enabled is uniform.
package tools

import (
	"cmp"
	"context"
	"encoding/json"
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

func registerTraceTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
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
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, grafana.DSKindTempo)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			q := url.Values{"q": []string{query}}
			if s := req.GetString("start", ""); s != "" {
				q.Set("start", s)
			}
			if e := req.GetString("end", ""); e != "" {
				q.Set("end", e)
			}
			if lim := req.GetInt("limit", 0); lim > 0 {
				q.Set("limit", strconv.Itoa(lim))
			}
			body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/search", q)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("tempo search", err), nil
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
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			names, err := fetchTempoTags(ctx, az, gc, orgRef, "", req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(paginateStrings(names, "", 0, len(names)))
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
			orgRef, err := req.RequireString("org")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tag, err := req.RequireString("tag")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			values, err := fetchTempoTags(ctx, az, gc, orgRef, tag, req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(paginateStrings(values, req.GetString("prefix", ""), req.GetInt("page", 0), req.GetInt("pageSize", 0)))
		},
	)
}

// qualifyTempoTag prepends the scope to a Tempo tag so list_tempo_tag_values
// can round-trip the result back to /api/v2/search/tag/{name}/values, which
// requires scope-qualified names. Tags already carrying ":" (e.g.
// "span:duration") are passed through; intrinsic and unscoped tags stay
// bare because Tempo's v2 endpoint accepts those without the prefix.
func qualifyTempoTag(scope, tag string) string {
	if tag == "" || strings.Contains(tag, ":") {
		return tag
	}
	if scope == "" || strings.EqualFold(scope, "intrinsic") {
		return tag
	}
	return scope + "." + tag
}

// fetchTempoTags hits /api/v2/search/tags (when tag is "") or
// /api/v2/search/tag/{tag}/values. Tempo's v2 API returns a single-level
// {scopes:[{name, tags:[...]}]} structure for tag names and
// {tagValues:[{type, value}]} for values; we flatten both to a []string.
func fetchTempoTags(ctx context.Context, az authz.Authorizer, gc grafana.Client, orgRef, tag string, req mcp.CallToolRequest) ([]string, error) {
	org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, grafana.DSKindTempo)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("start", cmp.Or(req.GetString("start", ""), fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())))
	q.Set("end", cmp.Or(req.GetString("end", ""), fmt.Sprintf("%d", time.Now().Unix())))
	if scope := req.GetString("scope", ""); scope != "" && scope != filterAll {
		q.Set("scope", scope)
	}

	if tag == "" {
		body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/v2/search/tags", q)
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
		// Tempo's v2 tag-values endpoint requires scope-qualified names
		// (e.g. "resource.service.name", "span.http.method"); intrinsics
		// stay bare. Preserve scoping here so list_tempo_tag_values gets
		// a usable identifier round-trip.
		var out []string
		for _, s := range v2.Scopes {
			for _, t := range s.Tags {
				out = append(out, qualifyTempoTag(s.Name, t))
			}
		}
		return out, nil
	}

	// User-controlled tag goes through url.PathEscape — Tempo's v2 endpoint
	// requires the scope-qualified name in the URL.
	path := "api/v2/search/tag/" + url.PathEscape(tag) + "/values"
	body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, path, q)
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
	out := make([]string, 0, len(v2.TagValues))
	for _, tv := range v2.TagValues {
		out = append(out, tv.Value)
	}
	return out, nil
}
