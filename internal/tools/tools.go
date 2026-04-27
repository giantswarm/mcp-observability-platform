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
	// names by the surviving local handlers in NameContains specs. Bridged
	// tools use the typed authz.DatasourceKind enum instead.
	dsKindLoki  = "loki"
	dsKindTempo = "tempo"

	// Alertmanager "active" — shared between the filter enum and the AM v2
	// /alerts URL-parameter name (which happens to be the same literal).
	amActive = "active"

	// Generic "match everything" filter token used across alerts and
	// silences filters.
	filterAll = "all"
)

// RegisterAll wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding per-category file.
//
// Bridged categories (delegated to upstream grafana/mcp-grafana with
// our org→OrgID + datasource-UID resolution applied):
//   - dashboards, datasources, annotations, deeplinks, panel rendering
//   - Mimir Prometheus tools (metrics.go)
//   - Loki tools (logs.go)
//   - alert-rule reads via alerting_manage_rules (alerting.go)
//
// Local categories (no usable upstream equivalent — see plan/roadmap):
//   - list_orgs (Giant-Swarm-specific GrafanaOrganization CR access)
//   - Alertmanager v2 alerts (upstream covers OnCall, not Alertmanager)
//   - Tempo (upstream has no Tempo surface)
//   - triage co-pilots (Sift requires Grafana Cloud; ours mimic against
//     open-source primitives)
func RegisterAll(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client, br *upstream.Bridge) {
	registerOrgTools(s, az, br)
	registerDashboardTools(s, br)
	registerMetricsTools(s, br)
	registerLogTools(s, br)
	registerAlertingTools(s, br)
	registerTraceTools(s, az, gc)
	registerAlertTools(s, az, gc)
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
