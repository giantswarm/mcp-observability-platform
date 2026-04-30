// examples.go — RoleViewer, no datasource scope.
//
// get_query_examples is a static PromQL/LogQL/SQL syntax helper. The
// upstream handler is Grafana-independent (returns canned strings), so
// org binding is purely for surface uniformity — every tool the LLM
// sees takes "org" as its first arg.
package tools

import (
	mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func registerExampleTools(s *mcpsrv.MCPServer, b *gfBinder) {
	b.bindOrgTool(s, authz.RoleViewer, mcpgrafanatools.GetQueryExamples)
}
