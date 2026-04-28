// logs.go — RoleViewer, DSKindLoki.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func registerLogTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.QueryLokiLogs,
		mcpgrafanatools.QueryLokiStats,
		mcpgrafanatools.QueryLokiPatterns,
		mcpgrafanatools.ListLokiLabelNames,
		mcpgrafanatools.ListLokiLabelValues,
	} {
		b.bindDatasourceTool(s, authz.RoleViewer, grafana.DSKindLoki, datasourceUIDArg, t)
	}
}
