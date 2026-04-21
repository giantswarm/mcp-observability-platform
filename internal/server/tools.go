// Package server wires the MCP protocol layer for this MCP.
//
// tools.go holds only shared helpers used by tool handlers. Each category of
// tool (orgs, dashboards, metrics, logs, traces, alerts, panels) lives in its
// own tools_*.go file, and registers itself via its own register*Tools
// function called from registerTools below.
package server

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// tracer is the OTEL tracer scoped to the server package; spans emitted here
// carry the MCP tool boundary and wrap downstream Grafana calls.
var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/server")

// registerTools wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding tools_*.go file.
func registerTools(s *mcpsrv.MCPServer, d *deps) {
	registerOrgTools(s, d)
	registerDashboardTools(s, d)
	registerMetricsTools(s, d)
	registerLogTools(s, d)
	registerTraceTools(s, d)
	registerAlertTools(s, d)
	registerSilenceTools(s, d)
	registerPanelTools(s, d)
}

// maxResponseBytes returns the configured cap on tool response body size.
// Set TOOL_MAX_RESPONSE_BYTES to 0 to disable. Default 131072 (128 KiB) —
// enough for most structured responses, small enough that a pathologically
// broad query like `up` on a large cluster returns a useful error instead of
// flooding the LLM context.
//
// Note: env-read per call is intentional — the cost is sub-microsecond and
// tests need to flip the value via t.Setenv between subtests. Caching via
// sync.OnceValue would break TestEnforceResponseCap_DisabledWithZero.
func maxResponseBytes() int {
	if v := os.Getenv("TOOL_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 128 * 1024
}

// responseCapError is the structured JSON payload returned when a tool
// response would exceed the configured cap. LLM clients see a typed error
// they can react to (by narrowing the query) rather than a truncated result.
type responseCapError struct {
	Error   string `json:"error"` // always "response_too_large"
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func enforceResponseCap(body []byte) *responseCapError {
	limit := maxResponseBytes()
	if limit <= 0 || len(body) <= limit {
		return nil
	}
	return &responseCapError{
		Error:   "response_too_large",
		Bytes:   len(body),
		Limit:   limit,
		Message: fmt.Sprintf("response is %d bytes, exceeds %d byte limit", len(body), limit),
		Hint:    "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range",
	}
}

// instrument wraps a tool handler so every call updates the tool-call counter
// and latency histogram with the final outcome (ok|err). Also opens a server
// span per call so downstream Grafana requests are correlated. Panics in the
// handler are NOT recovered — a panicking tool tears down the serving
// goroutine. A recovery middleware is planned for the composable-middleware
// PR; until then tool handlers must not panic on input the user controls.
func instrument(name string, d *deps, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "tool."+name)
		defer span.End()
		start := time.Now()
		res, err := h(ctx, req)
		outcome := "ok"
		if err != nil || (res != nil && res.IsError) {
			outcome = "err"
			span.SetStatus(codes.Error, "tool returned error")
		}
		dur := time.Since(start).Seconds()
		observability.ToolCallTotal.WithLabelValues(name, outcome).Inc()
		observability.ToolCallDuration.WithLabelValues(name, outcome).Observe(dur)
		if d.log != nil {
			d.log.Debug("tool call",
				"tool", name,
				"outcome", outcome,
				"duration_s", dur,
				"caller", callerSubject(ctx),
			)
		}
		return res, err
	}
}

// withToolTimeout returns a derived context that enforces a per-tool handler
// deadline. A bounded budget keeps a pathological LogQL query from holding
// the MCP goroutine open until the Grafana HTTP client times out at 30s.
func withToolTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// grafanaOpts packages orgID and caller-subject into a RequestOpts so every
// downstream call attributes to the caller via X-Grafana-User.
func grafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: callerSubject(ctx)}
}

// ---------- argument extraction ----------

func intArg(args map[string]any, name string) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

func strArg(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}

// requireOrg is the canonical arg-extraction error for tools that need an
// "org" argument. Returning an *mcp.CallToolResult lets callers pattern-match
// a single return statement.
func requireOrg(args map[string]any) (string, *mcp.CallToolResult) {
	org := strArg(args, "org")
	if org == "" {
		return "", mcp.NewToolResultError("missing required argument 'org'")
	}
	return org, nil
}

// clampInt clamps n into [min, max]. Used for pagination sizes.
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// ---------- datasource resolution + generic proxy handler ----------

// resolveDatasource runs the three checks every datasource-facing tool needs
// in one shot: the caller must have >= role on org, the org must host the
// required tenant type (empty = skip), and a datasource whose name contains
// all nameContains substrings (case-insensitive) must exist. Errors are
// caller-ready strings so handlers can surface them unchanged.
func resolveDatasource(ctx context.Context, d *deps, org string, role authz.Role, tenantType obsv1alpha2.TenantType, nameContains ...string) (authz.OrgAccess, int64, error) {
	oa, err := d.resolver.Require(ctx, callerAuthz(ctx), org, role)
	if err != nil {
		return authz.OrgAccess{}, 0, err
	}
	if tenantType != "" && !oa.HasTenantType(tenantType) {
		return authz.OrgAccess{}, 0, fmt.Errorf("org %q has no tenant of type %q — tool unavailable", org, tenantType)
	}
	dsID, ok := oa.FindDatasourceID(nameContains...)
	if !ok {
		return authz.OrgAccess{}, 0, fmt.Errorf("no datasource matching %v in org %q", nameContains, org)
	}
	return oa, dsID, nil
}

// datasourceSpec declaratively describes a tool that proxies to a Grafana
// datasource: which datasource to pick, which tenant type is required, and
// which API path/args to use on the downstream.
type datasourceSpec struct {
	Role            authz.Role
	NeedTenant      obsv1alpha2.TenantType
	NameContains    []string
	InstantPath     string
	RangePath       string
	SupportsRange   bool
	ForceRange      bool          // always use RangePath; fill in defaults if start/end missing
	DefaultRangeAgo time.Duration // default start = now - DefaultRangeAgo when ForceRange
	QueryArg        string        // name of the arg on the downstream API that carries the query expression
	ExtraArg        string        // optional pass-through arg (e.g. "limit")
	Timeout         time.Duration // per-tool handler budget; 0 = default 30s
}

// datasourceProxyHandler returns a tool handler that: authorises the caller
// on the given org, picks the correct datasource, validates tenant type, and
// forwards the query through Grafana's datasource proxy.
func datasourceProxyHandler(d *deps, spec datasourceSpec) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		org, errRes := requireOrg(args)
		if errRes != nil {
			return errRes, nil
		}
		oa, dsID, err := resolveDatasource(ctx, d, org, spec.Role, spec.NeedTenant, spec.NameContains...)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		budget := spec.Timeout
		if budget == 0 {
			budget = 30 * time.Second
		}
		ctx, cancel := withToolTimeout(ctx, budget)
		defer cancel()

		q := url.Values{}
		if spec.QueryArg != "" {
			if v := strArg(args, "query"); v != "" {
				q.Set(spec.QueryArg, v)
			}
		}
		start := strArg(args, "start")
		end := strArg(args, "end")
		path := spec.InstantPath
		useRange := spec.SupportsRange && (spec.ForceRange || (start != "" && end != ""))
		if useRange {
			path = spec.RangePath
			// Backfill both start + end whenever the spec declares a default
			// range, not only when ForceRange is set. Grafana rejects range
			// queries with a missing end; anchoring both ends here keeps the
			// shape stable regardless of which flag opted the tool in.
			if start == "" && spec.DefaultRangeAgo > 0 {
				start = fmt.Sprintf("%d", time.Now().Add(-spec.DefaultRangeAgo).UnixNano())
			}
			if end == "" && (spec.ForceRange || spec.DefaultRangeAgo > 0) {
				end = fmt.Sprintf("%d", time.Now().UnixNano())
			}
			q.Set("start", start)
			q.Set("end", end)
			if step := strArg(args, "step"); step != "" {
				q.Set("step", step)
			}
		} else {
			if start != "" {
				q.Set("start", start)
			}
			if end != "" {
				q.Set("end", end)
			}
		}
		if spec.ExtraArg != "" {
			if v := intArg(args, spec.ExtraArg); v > 0 {
				q.Set(spec.ExtraArg, fmt.Sprintf("%d", v))
			}
		}

		observability.GrafanaProxyTotal.WithLabelValues(spec.InstantPath).Inc()
		dsStart := time.Now()
		body, err := d.grafana.DatasourceProxy(ctx, grafanaOpts(ctx, oa.OrgID), dsID, path, q)
		observability.GrafanaProxyDuration.WithLabelValues(spec.InstantPath).Observe(time.Since(dsStart).Seconds())
		if err != nil {
			return mcp.NewToolResultErrorFromErr("grafana datasource proxy failed", err), nil
		}
		if capErr := enforceResponseCap(body); capErr != nil {
			return mcp.NewToolResultJSON(capErr)
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}

// ---------- pagination helpers for list-of-string results ----------

// paginatedStrings is the JSON projection used by every "list_*" tool that
// returns a flat list of strings (metric names, label values, tag values…).
type paginatedStrings struct {
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	HasMore  bool     `json:"hasMore"`
	Items    []string `json:"items"`
}

// paginateStrings slices values[] into a page. If prefix is non-empty, only
// values whose lowercase form contains the lowercase prefix are kept (applied
// before paging so totals are accurate). Output is always sorted alphabetically.
//
// A defensive copy is made before sorting so callers can pass a slice backed
// by a cache (e.g. the resolver's org list) without having their cache
// reordered as a side effect.
func paginateStrings(values []string, prefix string, page, pageSize int) paginatedStrings {
	if prefix != "" {
		lp := strings.ToLower(prefix)
		filtered := make([]string, 0, len(values))
		for _, v := range values {
			if strings.Contains(strings.ToLower(v), lp) {
				filtered = append(filtered, v)
			}
		}
		values = filtered
	} else {
		values = append([]string(nil), values...)
	}
	sort.Strings(values)
	if pageSize <= 0 {
		pageSize = 100
	}
	pageSize = clampInt(pageSize, 1, 1000)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(values))
	end := min(start+pageSize, len(values))
	return paginatedStrings{
		Total:    len(values),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(values),
		Items:    values[start:end],
	}
}
