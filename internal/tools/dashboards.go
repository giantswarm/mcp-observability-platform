// Package tools — dashboards.go: dashboards + navigation tools, all
// delegated to upstream grafana/mcp-grafana via the bridge. Only the
// local "org" arg is added on top of upstream's schemas.
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
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

// registerDashboardTools wires the upstream dashboard + navigation tools
// onto our MCP server. All gate on RoleViewer; the bridge handles
// org→OrgID resolution and X-Grafana-User caller attribution before
// delegating.
func registerDashboardTools(s *mcpsrv.MCPServer, br *upstream.Bridge) {
	for _, t := range []mcpgrafana.Tool{
		mcpgrafanatools.SearchDashboards,
		mcpgrafanatools.SearchFolders,
		mcpgrafanatools.GetDashboardByUID,
		mcpgrafanatools.GetDashboardSummary,
		mcpgrafanatools.GetDashboardPanelQueries,
		mcpgrafanatools.GetDashboardProperty,
		mcpgrafanatools.GenerateDeeplink,
	} {
		s.AddTool(upstream.WithOrg(t.Tool, ""), br.Wrap(authz.RoleViewer, "", "", t))
	}
}
