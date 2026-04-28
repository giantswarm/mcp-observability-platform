// Package tools — triage.go: SRE-triage co-pilot tools.
//
// find_error_pattern_logs and find_slow_requests deliberately keep the
// upstream grafana/mcp-grafana tool names + descriptions so an LLM
// prompt written for Grafana Cloud works unchanged here. Upstream's
// implementations of those tools delegate to Grafana Cloud's managed
// **Sift** backend, which isn't reachable from a self-hosted
// Mimir/Loki/Tempo install — so we can't bridge to upstream and have
// to compute equivalent results locally against the open-source
// primitives. Same surface, different back end. If a future
// open-sourced Sift becomes available, the bridge can replace these
// handlers verbatim.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

const (
	errorPatternRegex        = `(?i)(error|fail|fatal|panic|exception|traceback)`
	findErrorPatternMaxBytes = 256 << 20
	defaultTriageLookback    = 15 * time.Minute
)

// serviceLabelCandidates are probed in order; OTel emits service_name,
// older Prometheus-style streams use service or job.
var serviceLabelCandidates = []string{"service_name", "service", "job"}

// registerTriageTools wires the three triage tools into s.
func registerTriageTools(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client) {
	s.AddTool(
		mcp.NewTool("find_error_pattern_logs",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Find recent error / fail / fatal / panic / exception log lines for a service in Loki. Auto-picks the right service label (service_name → service → job) and runs Loki's size-estimate first; refuses to fire when the window is too large. Use when a user asks 'what's broken with X?' — short-circuits writing a LogQL regex by hand."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("service", mcp.Required(), mcp.Description("Service identifier as it appears in service_name / service / job.")),
			mcp.WithString("lookback", mcp.Description("Go duration; default 15m (e.g. '5m', '1h').")),
		),
		findErrorPatternLogsHandler(az, gc),
	)

	s.AddTool(
		mcp.NewTool("find_slow_requests",
			ReadOnlyAnnotation(),
			mcp.WithDescription("Find traces in Tempo where a service's spans took longer than min_duration. Optional errors_only filter narrows to status=error. Defaults: min_duration=1s, lookback=15m, limit=20."),
			mcp.WithString("org", mcp.Required(), mcp.Description("Organization — see list_orgs.")),
			mcp.WithString("service", mcp.Required(), mcp.Description("Value of resource.service.name to filter on.")),
			mcp.WithString("lookback", mcp.Description("Go duration; default 15m.")),
			mcp.WithString("min_duration", mcp.Description("Go duration; default 1s (e.g. '500ms', '2s').")),
			mcp.WithBoolean("errors_only", mcp.Description("If true, filter to status=error spans only.")),
		),
		findSlowRequestsHandler(az, gc),
	)
}

// findErrorPatternLogsHandler implements find_error_pattern_logs: probe a
// service label, build an error-keyword regex selector, size-check via Loki
// stats, then run query_range with limit=100.
func findErrorPatternLogsHandler(az authz.Authorizer, gc grafana.Client) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		service, err := req.RequireString("service")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lookback, err := parseDurationOrDefault(req.GetString("lookback", ""), defaultTriageLookback)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("lookback: %v", err)), nil
		}

		org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindLoki)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ctx, cancel := withToolTimeout(ctx, 30*time.Second)
		defer cancel()

		end := time.Now()
		start := end.Add(-lookback)
		startNs := strconv.FormatInt(start.UnixNano(), 10)
		endNs := strconv.FormatInt(end.UnixNano(), 10)

		label, err := pickServiceLabel(ctx, gc, org.OrgID, dsID, service, startNs, endNs)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("loki label probe", err), nil
		}
		if label == "" {
			return mcp.NewToolResultError(fmt.Sprintf("service %q not found under any of %v in the last %s — check spelling, widen lookback, or list streams via list_loki_label_values", service, serviceLabelCandidates, lookback)), nil
		}

		selector := fmt.Sprintf(`{%s=%q} |~ %q`, label, service, errorPatternRegex)

		statsQ := url.Values{}
		statsQ.Set("query", selector)
		statsQ.Set("start", startNs)
		statsQ.Set("end", endNs)
		observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/index/stats").Inc()
		statsBody, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "loki/api/v1/index/stats", statsQ)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("loki stats", err), nil
		}
		var stats struct {
			Bytes int64 `json:"bytes"`
		}
		if err := json.Unmarshal(statsBody, &stats); err != nil {
			return mcp.NewToolResultErrorFromErr("parse loki stats", err), nil
		}
		if stats.Bytes > findErrorPatternMaxBytes {
			return mcp.NewToolResultJSON(struct {
				Error          string `json:"error"`
				Hint           string `json:"hint"`
				EstimatedBytes int64  `json:"estimated_bytes"`
				EstimatedHuman string `json:"estimated_human"`
				Selector       string `json:"selector"`
				Lookback       string `json:"lookback"`
			}{
				Error:          "estimated_size_too_large",
				Hint:           "narrow the lookback or apply an additional label filter (e.g. namespace, level)",
				EstimatedBytes: stats.Bytes,
				EstimatedHuman: humanBytes(stats.Bytes),
				Selector:       selector,
				Lookback:       lookback.String(),
			})
		}

		rangeQ := url.Values{}
		rangeQ.Set("query", selector)
		rangeQ.Set("start", startNs)
		rangeQ.Set("end", endNs)
		rangeQ.Set("limit", "100")
		rangeQ.Set("direction", "backward")
		observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/query_range").Inc()
		body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "loki/api/v1/query_range", rangeQ)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("loki query_range", err), nil
		}
		return mcp.NewToolResultJSON(struct {
			Selector       string          `json:"selector"`
			Lookback       string          `json:"lookback"`
			EstimatedBytes int64           `json:"estimated_bytes"`
			Data           json.RawMessage `json:"data"`
		}{
			Selector:       selector,
			Lookback:       lookback.String(),
			EstimatedBytes: stats.Bytes,
			Data:           body,
		})
	}
}

// findSlowRequestsHandler implements find_slow_requests: build a TraceQL
// expression filtering on resource.service.name + duration (+ status=error
// when errors_only), then call Tempo /api/search.
func findSlowRequestsHandler(az authz.Authorizer, gc grafana.Client) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		service, err := req.RequireString("service")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lookback, err := parseDurationOrDefault(req.GetString("lookback", ""), defaultTriageLookback)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("lookback: %v", err)), nil
		}
		minDur, err := parseDurationOrDefault(req.GetString("min_duration", ""), time.Second)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("min_duration: %v", err)), nil
		}
		errorsOnly := req.GetBool("errors_only", false)

		org, dsID, err := resolveDatasource(ctx, az, orgRef, authz.RoleViewer, authz.TenantTypeData, dsKindTempo)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ctx, cancel := withToolTimeout(ctx, 20*time.Second)
		defer cancel()

		traceQL := buildSlowRequestsTraceQL(service, minDur, errorsOnly)
		end := time.Now()
		start := end.Add(-lookback)
		q := url.Values{}
		q.Set("q", traceQL)
		q.Set("start", strconv.FormatInt(start.Unix(), 10))
		q.Set("end", strconv.FormatInt(end.Unix(), 10))
		q.Set("limit", "20")
		observability.GrafanaProxyTotal.WithLabelValues("api/search").Inc()
		body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, org.OrgID), dsID, "api/search", q)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("tempo search", err), nil
		}
		return mcp.NewToolResultJSON(struct {
			Query    string          `json:"query"`
			Lookback string          `json:"lookback"`
			Data     json.RawMessage `json:"data"`
		}{Query: traceQL, Lookback: lookback.String(), Data: body})
	}
}

// pickServiceLabel returns the first label in serviceLabelCandidates whose
// values list contains service. Returns ("", nil) when none match,
// ("", err) only if every candidate's HTTP/JSON probe failed.
func pickServiceLabel(ctx context.Context, gc grafana.Client, orgID, dsID int64, service, start, end string) (string, error) {
	var lastErr error
	for _, label := range serviceLabelCandidates {
		q := url.Values{}
		q.Set("start", start)
		q.Set("end", end)
		path := "loki/api/v1/label/" + url.PathEscape(label) + "/values"
		observability.GrafanaProxyTotal.WithLabelValues("loki/api/v1/label/:name/values").Inc()
		body, err := gc.DatasourceProxy(ctx, grafanaOpts(ctx, orgID), dsID, path, q)
		if err != nil {
			lastErr = err
			continue
		}
		var env struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			lastErr = err
			continue
		}
		if slices.Contains(env.Data, service) {
			return label, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

// buildSlowRequestsTraceQL builds the TraceQL for find_slow_requests.
// strconv.Quote on service so embedded quotes/backslashes can't break the
// expression.
func buildSlowRequestsTraceQL(service string, minDur time.Duration, errorsOnly bool) string {
	var b strings.Builder
	b.WriteString(`{ resource.service.name = `)
	b.WriteString(strconv.Quote(service))
	b.WriteString(` && duration > `)
	b.WriteString(minDur.String())
	if errorsOnly {
		b.WriteString(` && status = error`)
	}
	b.WriteString(` }`)
	return b.String()
}

// parseDurationOrDefault parses a Go duration. Empty input returns def.
// Non-empty inputs that fail to parse or are non-positive return an error.
func parseDurationOrDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", s)
	}
	return d, nil
}

// humanBytes formats n in IEC units (e.g. "512.0 MiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
