// Package tools — dashboards.go: dashboards / annotations / deeplinks /
// panel-rendering tools, all delegated to upstream grafana/mcp-grafana
// via the bridge. Only the local "org" arg is added on top of upstream's
// schemas.
package tools

import (
	mcpgrafana "github.com/grafana/mcp-grafana"
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

// registerDashboardTools wires the upstream dashboard, search,
// annotations, navigation, and rendering tools onto our MCP server. All
// tools gate on RoleViewer; the bridge handles org→OrgID resolution and
// X-Grafana-User caller attribution before delegating.
func registerDashboardTools(s *mcpsrv.MCPServer, br *upstream.Bridge) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.SearchDashboards,
		mcpgrafanatools.SearchFolders,
		mcpgrafanatools.GetDashboardByUID,
		mcpgrafanatools.GetDashboardSummary,
		mcpgrafanatools.GetDashboardPanelQueries,
		mcpgrafanatools.GetDashboardProperty,
		mcpgrafanatools.GetAnnotationsTool,
		mcpgrafanatools.GetAnnotationTagsTool,
		mcpgrafanatools.GenerateDeeplink,
		mcpgrafanatools.GetPanelImage,
	} {
		s.AddTool(upstream.WithOrg(t.Tool), br.Wrap(authz.RoleViewer, t))
	}
}
