// Package tools — logs.go: Loki query/label/stats/pattern tools, all
// delegated to upstream grafana/mcp-grafana. The local "org" argument
// is the only schema addition; the binder resolves it to the org's
// Loki datasource UID and injects datasourceUid before invoking
// upstream's handler.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// registerLogTools wires the upstream Loki tools onto our MCP server.
// All gate on RoleViewer; the binder handles org→OrgID resolution,
// datasource UID injection, and X-Grafana-User caller attribution.
func registerLogTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.QueryLokiLogs,
		mcpgrafanatools.QueryLokiStats,
		mcpgrafanatools.QueryLokiPatterns,
		mcpgrafanatools.ListLokiLabelNames,
		mcpgrafanatools.ListLokiLabelValues,
	} {
		b.bindDatasourceTool(s, authz.RoleViewer, authz.DSKindLoki, datasourceUIDArg, t)
	}
}
