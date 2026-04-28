// alerting.go — RoleViewer, DSKindMimir.
//
// alerting_manage_rules is upstream's meta-tool; the `operation` enum
// (list/get/versions) is its surface, not ours. We expose the read
// variant only.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func registerAlertingTools(s *mcpsrv.MCPServer, b *gfBinder) {
	// alerting_manage_rules uses snake_case "datasource_uid" — every other
	// upstream tool uses camelCase "datasourceUid".
	b.bindDatasourceTool(s, authz.RoleViewer, authz.TenantTypeData, grafana.DSKindMimir, "datasource_uid", mcpgrafanatools.ManageRulesRead)
}
