// alerting.go — RoleViewer.
//
// alerting_manage_rules is upstream's meta-tool; the `operation` enum
// (list/get/versions) is its surface, not ours. We expose the read
// variant only, fanned out across every ruler-capable datasource the
// org has where jsonData.manageAlerts is true. `get`/`versions`
// require Grafana-managed RuleUIDs, not datasource-side rule names.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func registerAlertingTools(s *mcpsrv.MCPServer, b *gfBinder) {
	// alerting_manage_rules uses snake_case "datasource_uid" — every other
	// upstream tool uses camelCase "datasourceUid".
	b.bindDatasourceFanoutTool(s, authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, mcpgrafanatools.ManageRulesRead)
}
