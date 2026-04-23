// Package tools wires the MCP tool surface of this MCP.
//
// tools.go holds the package entry point — the exported Deps struct that the
// server composition root hands in, the package-wide constants, and the
// RegisterAll dispatcher. Tool handlers live in per-category files
// (alerts.go, dashboards.go, …), shared helpers in focused files
// (datasource.go, pagination.go, response_cap.go, timeout.go, annotations.go).
package tools

import (
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
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

// Deps bundles the handler-scoped dependencies so tool registration stays
// concise. Exported so the server package can build one and hand it off to
// RegisterAll. The fields are interfaces declared in their producer
// packages (`authz.Authorizer`, `grafana.Client`) so tests can supply mocks
// without re-declaring method sets here.
type Deps struct {
	Authorizer authz.Authorizer
	Grafana    grafana.Client
}

// Package-wide string tokens. Kept as untyped constants so they drop into
// []string{...} literals and switch arms cleanly.
const (
	// Datasource kind tokens, substring-matched against Grafana datasource
	// names in NameContains specs and produced by datasourceKindFromRef.
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

	// Grafana panel type for legacy nested rows.
	panelTypeRow = "row"
)

// RegisterAll wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding per-category file.
func RegisterAll(s *mcpsrv.MCPServer, d *Deps) {
	registerOrgTools(s, d)
	registerDashboardTools(s, d)
	registerMetricsTools(s, d)
	registerLogTools(s, d)
	registerTraceTools(s, d)
	registerAlertTools(s, d)
	registerSilenceTools(s, d)
	registerPanelTools(s, d)
}
