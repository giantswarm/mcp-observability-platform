// alerting.go — RoleViewer.
//
// alerting_rules_read is upstream's read-only rules tool; the `operation` enum
// (list/get/versions) is its surface, not ours. We expose the read
// tool only, fanned out across every ruler-capable datasource the
// org has where jsonData.manageAlerts is true. `get`/`versions`
// require Grafana-managed RuleUIDs, not datasource-side rule names.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/v2/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func registerAlertingTools(s *mcpsrv.MCPServer, b *gfBinder) {
	// alerting_rules_read uses snake_case "datasource_uid" — every other
	// upstream tool uses camelCase "datasourceUid".
	b.bindDatasourceFanoutTool(s, authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, mcpgrafanatools.AlertRulesRead)
}
