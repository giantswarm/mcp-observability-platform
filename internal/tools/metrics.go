// metrics.go — RoleViewer, prometheus-typed datasources.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func registerMetricsTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.QueryPrometheus,
		mcpgrafanatools.QueryPrometheusHistogram,
		mcpgrafanatools.ListPrometheusMetricNames,
		mcpgrafanatools.ListPrometheusLabelNames,
		mcpgrafanatools.ListPrometheusLabelValues,
		mcpgrafanatools.ListPrometheusMetricMetadata,
	} {
		b.bindDatasourceTool(s, authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg, t)
	}
}
