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

// registerAlertingTools wires the upstream alert-rule reader. The
// binder injects "datasource_uid" (snake_case, upstream's choice)
// with the org's Mimir UID before calling upstream.
func registerAlertingTools(s *mcpsrv.MCPServer, b *gfBinder) {
	// alerting_manage_rules uses snake_case (vs the typical "datasourceUid").
	b.bindDatasourceTool(s, authz.RoleViewer, grafana.DSKindMimir, "datasource_uid", mcpgrafanatools.ManageRulesRead)
}
