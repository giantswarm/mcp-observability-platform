// dashboards.go — RoleViewer, no datasource scope.
//
// Skipped on purpose:
//   - get_panel_image — LLMs can't reliably interpret PNG bytes through
//     MCP; the tool is rendered for human consumption only.
//   - get_annotations / get_annotation_tags — niche dashboard-tooling
//     surface; reinstate via this list when an LLM use case shows up.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func registerDashboardTools(s *mcpsrv.MCPServer, b *gfBinder) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.SearchDashboards,
		mcpgrafanatools.SearchFolders,
		mcpgrafanatools.GetDashboardByUID,
		mcpgrafanatools.GetDashboardSummary,
		mcpgrafanatools.GetDashboardPanelQueries,
		mcpgrafanatools.GetDashboardProperty,
		mcpgrafanatools.GenerateDeeplink,
	} {
		b.bindOrgTool(s, authz.RoleViewer, t)
	}
}
