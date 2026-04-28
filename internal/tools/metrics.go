// Package tools — metrics.go: Mimir Prometheus tools, all delegated to
// upstream grafana/mcp-grafana. The local "org" arg is the only schema
// addition; the binder resolves it to the org's Mimir datasource UID
// and injects datasourceUid before invoking upstream.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// registerMetricsTools wires the upstream Mimir/Prometheus tools onto
// our MCP server. All gate on RoleViewer; the binder handles
// org→OrgID + Mimir-datasource-UID resolution and X-Grafana-User
// caller attribution.
func registerMetricsTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.QueryPrometheus,
		mcpgrafanatools.QueryPrometheusHistogram,
		mcpgrafanatools.ListPrometheusMetricNames,
		mcpgrafanatools.ListPrometheusLabelNames,
		mcpgrafanatools.ListPrometheusLabelValues,
		mcpgrafanatools.ListPrometheusMetricMetadata,
	} {
		b.bindDatasourceTool(s, authz.RoleViewer, grafana.DSKindMimir, datasourceUIDArg, t)
	}
}
