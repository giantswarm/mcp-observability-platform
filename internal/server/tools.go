// Package server wires the MCP protocol layer for this MCP.
//
// tools.go is just the top-level registration dispatch. Each category of
// tool lives in its own tools_*.go file and registers via its own
// registerXxxTools function called from registerTools below. Shared
// middleware + helpers live in internal/tools/middleware.
package server

import (
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/tools/middleware"
)

func registerTools(s *mcpsrv.MCPServer, d *middleware.Deps) {
	registerOrgTools(s, d)
	registerDashboardTools(s, d)
	registerMetricsTools(s, d)
	registerLogTools(s, d)
	registerTraceTools(s, d)
	registerAlertTools(s, d)
	registerSilenceTools(s, d)
	registerPanelTools(s, d)
}
