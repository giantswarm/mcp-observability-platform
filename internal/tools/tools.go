// Package tools wires the MCP tool surface of this MCP.
//
// tools.go holds the package entry point — the exported Deps struct that the
// server composition root hands in, the package-wide constants, and the
// RegisterAll dispatcher. Tool handlers live in per-category files
// (alerts.go, dashboards.go, …), shared helpers in focused files
// (datasource.go, pagination.go, response_cap.go, timeout.go, annotations.go).
package tools

import (
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// Deps bundles the handler-scoped dependencies so tool registration stays
// concise. Exported so the server package can build one and hand it off to
// RegisterAll.
type Deps struct {
	Resolver *authz.Resolver
	Grafana  *grafana.Client
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
