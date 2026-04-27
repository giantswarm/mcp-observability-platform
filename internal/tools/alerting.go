// Package tools — alerting.go: Mimir/Loki alert-rule reads, delegated to
// upstream grafana/mcp-grafana via the bridge.
//
// alerting_manage_rules is upstream's read-only meta-tool for browsing
// rules: it covers what our prior list_alert_rules + get_alert_rule
// pair did, behind a single tool with an `operation` enum
// (list/get/versions). Bridged with the Mimir datasource scoped from
// the caller's org.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

// registerAlertingTools wires the upstream alert-rule reader. The
// bridge injects "datasource_uid" (snake_case, upstream's choice) with
// the org's Mimir UID before calling upstream.
func registerAlertingTools(s *mcpsrv.MCPServer, br *upstream.Bridge) {
	t := mcpgrafanatools.ManageRulesRead
	s.AddTool(
		upstream.WithOrgReplacingArg(t.Tool, "datasource_uid"),
		br.WrapDatasourceArg(authz.RoleViewer, authz.DSKindMimir, "datasource_uid", t),
	)
}
