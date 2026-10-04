// tempo.go — RoleViewer, tempo-typed datasources.
//
// Upstream mcp-grafana's native Tempo tools call Tempo's HTTP API
// through Grafana's datasource proxy. get_tempo_traceql_docs returns
// static reference text and takes no datasource, so it's bound
// org-only.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana/v2"
	mcpgrafanatools "github.com/grafana/mcp-grafana/v2/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func registerTempoTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.SearchTempoTracesTool,
		mcpgrafanatools.QueryTempoMetricsTool,
		mcpgrafanatools.GetTempoTraceTool,
		mcpgrafanatools.DiffTempoTracesTool,
		mcpgrafanatools.ListTempoAttributeNamesTool,
		mcpgrafanatools.ListTempoAttributeValuesTool,
	} {
		b.bindDatasourceTool(s, authz.RoleViewer, authz.TenantTypeData, grafana.DSTypeTempo, datasourceUIDArg, t)
	}
	b.bindOrgTool(s, authz.RoleViewer, mcpgrafanatools.GetTempoTraceQLDocsTool)
}
