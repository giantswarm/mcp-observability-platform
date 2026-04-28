// Package tools — alerting.go: Mimir/Loki alert-rule reads via
// upstream's alerting_manage_rules meta-tool. Read-only; the
// `operation` enum (list/get/versions) is upstream's surface — we just
// bind it to the caller's org → Mimir datasource.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// registerAlertingTools wires the upstream alert-rule reader. The
// binder injects "datasource_uid" (snake_case, upstream's choice)
// with the org's Mimir UID before calling upstream.
func registerAlertingTools(s *mcpsrv.MCPServer, b *gfBinder) {
	// alerting_manage_rules uses snake_case (vs the typical "datasourceUid").
	b.bindDatasourceTool(s, authz.RoleViewer, authz.DSKindMimir, "datasource_uid", mcpgrafanatools.ManageRulesRead)
}
