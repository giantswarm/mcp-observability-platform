package tools

import (
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// filterAll is the "no filter" token shared between AM /alerts state
// filters (alerts.go) and Tempo tag scope filters (traces.go).
const filterAll = "all"

// amActive — shared between the AM v2 /alerts URL parameter
// ("active=true|false") and the LLM-facing state filter enum (which
// happens to use the same literal).
const amActive = "active"

// readOnlyAnnotation flags every tool in this MCP as read-only,
// open-world, and non-destructive. IdempotentHint is intentionally
// omitted — many tools (query_prometheus, query_loki_logs, list_alerts)
// return live data that changes between calls, so advertising
// idempotence across the surface would be wrong.
func readOnlyAnnotation() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    mcp.ToBoolPtr(true),
		OpenWorldHint:   mcp.ToBoolPtr(true),
		DestructiveHint: mcp.ToBoolPtr(false),
	})
}

// orgArg is the canonical "org" argument for local tools. Delegated
// tools build the same arg via withOrg in grafanabind.go.
func orgArg() mcp.ToolOption {
	return mcp.WithString("org", mcp.Required(), mcp.Description(orgArgDescription))
}

// RegisterAll wires every category of tool into the MCP server. See
// doc.go for the per-category breakdown of delegated vs local handlers.
func RegisterAll(s *mcpsrv.MCPServer, az authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) error {
	b, err := newGFBinder(az, gc, grafanaURL, apiKey, basicAuth)
	if err != nil {
		return err
	}
	registerOrgTools(s, az, b)
	registerDashboardTools(s, b)
	registerMetricsTools(s, b)
	registerLogTools(s, b)
	registerAlertingTools(s, b)
	registerTraceTools(s, az, gc)
	registerAlertTools(s, az, gc)
	return nil
}
