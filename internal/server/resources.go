// Package server — resources.go was previously where MCP resource templates
// lived. We dropped them in favour of equivalent tools (LLMs handle tools
// far more reliably than resources):
//
//   - observability://org/{name}                 → list_orgs + list_datasources
//   - alertmanager://org/{name}/alert/{fp}       → get_alert
//   - grafana://org/{name}/dashboard/{uid}       → get_dashboard_by_uid
//
// registerResources is kept as a no-op so the existing call site in server.go
// stays intact; future resource templates (if we add any) would go here.
package server

import (
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/middleware"
)

func registerResources(_ *mcpsrv.MCPServer, _ *middleware.Deps) {}
