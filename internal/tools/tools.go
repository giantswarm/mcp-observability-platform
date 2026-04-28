package tools

import (
	"context"
	"net/url"
	"time"

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

// ReadOnlyAnnotation is the MCP tool option that flags a tool as read-only,
// open-world, and non-destructive. Every tool in this MCP is read-only (no
// write operations by design) so this is applied uniformly at registration.
//
// IdempotentHint is intentionally omitted — many tools (query_prometheus,
// query_loki_logs, list_alerts) return live data that changes between calls,
// so advertising idempotence across the whole surface would be wrong.
func ReadOnlyAnnotation() mcp.ToolOption {
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

// RegisterAll wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding per-category file.
//
// Categories delegated to upstream grafana/mcp-grafana (our org→OrgID
// + datasource-UID resolution applied via gfBinder):
//   - dashboards, datasources, annotations, deeplinks, panel rendering
//   - Mimir Prometheus tools (metrics.go)
//   - Loki tools (logs.go)
//   - alert-rule reads via alerting_manage_rules (alerting.go)
//
// Local categories (no usable upstream equivalent):
//   - list_orgs (Giant-Swarm-specific GrafanaOrganization CR access)
//   - Alertmanager v2 alerts (upstream covers OnCall, not Alertmanager)
//   - Tempo (upstream has no Tempo surface)
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

// withToolTimeout returns a derived context that enforces a per-tool handler
// deadline. A bounded budget keeps a pathological LogQL query from holding
// the MCP goroutine open until the Grafana HTTP client times out at 30s.
// If ctx already has a deadline the function returns the original ctx and a
// no-op cancel.
func withToolTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
