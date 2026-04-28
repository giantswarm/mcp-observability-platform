package tools

import (
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// filterAll is the "no filter" token shared between AM /alerts state
// filters (alerts.go) and Tempo tag scope filters (traces.go).
const filterAll = "all"

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

// orgArg is the canonical "org" argument for local tools. Bridged tools
// build the same arg via withOrg in grafanabind.go.
func orgArg() mcp.ToolOption {
	return mcp.WithString("org", mcp.Required(), mcp.Description(orgArgDescription))
}

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
// Local categories (no usable upstream equivalent):
//   - list_orgs (Giant-Swarm-specific GrafanaOrganization CR access)
//   - Alertmanager v2 alerts (upstream covers OnCall, not Alertmanager)
//   - Tempo (upstream has no Tempo surface)
func RegisterAll(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) error {
	b, err := newGFBinder(az, gc, grafanaURL, apiKey, basicAuth)
	if err != nil {
		return err
	}
	registerOrgTools(s, az, b)
	registerDashboardTools(s, b)
	registerMetricsTools(s, b)
	registerLogTools(s, b)
	registerAlertingTools(s, b)
	registerTraceTools(s, az, gc)
	registerAlertTools(s, az, gc)
	return nil
}
