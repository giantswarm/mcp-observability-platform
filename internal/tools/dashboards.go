// dashboards.go — RoleViewer, no datasource scope.
//
// run_panel_query resolves its datasource internally from the dashboard
// JSON, so it's bound org-only — the LLM never picks a UID.
//
// get_panel_image hits Grafana's /render endpoint (not the datasource
// proxy). On clusters without the Grafana Image Renderer service the
// upstream handler returns a clear "image renderer not available"
// error — same failure shape as any tool called against a missing
// backend. Returns an MCP `ImageContent`; vision-capable clients
// render the PNG natively. ResponseCap only intercepts TextContent,
// so PNG bytes flow through uncapped — revisit if pathologically large
// renders bite in production.
//
// Skipped on purpose:
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
		mcpgrafanatools.RunPanelQuery,
		mcpgrafanatools.GetPanelImage,
	} {
		b.bindOrgTool(s, authz.RoleViewer, t)
	}
}
