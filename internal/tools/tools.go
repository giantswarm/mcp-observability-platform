package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

// ReadOnlyAnnotation is the MCP tool option that flags a tool as read-only,
// open-world, and non-destructive. Every tool in this MCP is read-only (no
// write operations by design) so this is applied uniformly at registration.
//
// IdempotentHint is intentionally omitted — many tools (query_prometheus,
// query_loki_logs, list_alerts) return live data that changes between calls,
// so advertising idempotence across the whole surface would be wrong.
func ReadOnlyAnnotation() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    mcp.ToBoolPtr(true),
		OpenWorldHint:   mcp.ToBoolPtr(true),
		DestructiveHint: mcp.ToBoolPtr(false),
	})
}

// Package-wide string tokens. Kept as untyped constants so they drop into
// []string{...} literals and switch arms cleanly.
const (
	// Datasource kind tokens, substring-matched against Grafana datasource
	// names in NameContains specs.
	dsKindMimir = "mimir"
	dsKindLoki  = "loki"
	dsKindTempo = "tempo"

	// Alertmanager "active" — shared between the filter enum and the AM v2
	// /alerts URL-parameter name (which happens to be the same literal).
	amActive = "active"

	// Generic "match everything" filter token used across alerts, silences,
	// and Prometheus-rule type/state filters.
	filterAll = "all"

	// Prometheus rule types as returned by Mimir's /api/v1/rules (after our
	// projection) and accepted by list_alert_rules / get_alert_rule.
	ruleTypeAlert  = "alert"
	ruleTypeRecord = "record"
)

// RegisterAll wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding per-category file.
//
// Org-only tools (dashboards, datasources, annotations, deeplinks, panel
// rendering) delegate to upstream grafana/mcp-grafana via br.Wrap.
// Tools that resolve a specific datasource by tenant + name match
// (Mimir/Loki/Tempo/Alertmanager queries, triage co-pilots) stay local
// for now — upstream takes a datasourceUid which we don't surface yet.
func RegisterAll(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client, br *upstream.Bridge) {
	registerOrgTools(s, br)
	registerDashboardTools(s, br)
	registerMetricsTools(s, az, gc)
	registerLogTools(s, az, gc)
	registerTraceTools(s, az, gc)
	registerAlertTools(s, az, gc)
	registerSilenceTools(s, az, gc)
	registerTriageTools(s, az, gc)
}

// withToolTimeout returns a derived context that enforces a per-tool handler
// deadline. A bounded budget keeps a pathological LogQL query from holding
// the MCP goroutine open until the Grafana HTTP client times out at 30s.
// If ctx already has a deadline the function returns the original ctx and a
// no-op cancel.
func withToolTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
