package tools

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// filterAll is the "no filter" token used by the AM /alerts state filter
// in alerts.go.
const filterAll = "all"

// amActive — shared between the AM v2 /alerts URL parameter
// ("active=true|false") and the LLM-facing state filter enum (which
// happens to use the same literal).
const amActive = "active"

// readOnlyToolAnnotation is the canonical annotation for every tool in
// this MCP: read-only, open-world, non-destructive. IdempotentHint is
// intentionally omitted — many tools (query_prometheus, query_loki_logs,
// list_alerts) return live data that changes between calls.
var readOnlyToolAnnotation = mcp.ToolAnnotation{
	ReadOnlyHint:    mcp.ToBoolPtr(true),
	OpenWorldHint:   mcp.ToBoolPtr(true),
	DestructiveHint: mcp.ToBoolPtr(false),
}

// readOnlyAnnotation is the mcp.NewTool option form of
// readOnlyToolAnnotation. Use the value directly when mutating an
// existing mcp.Tool (e.g. tools delegated through ProxiedClient).
func readOnlyAnnotation() mcp.ToolOption {
	return mcp.WithToolAnnotation(readOnlyToolAnnotation)
}

// orgArg is the canonical "org" argument for local tools. Delegated
// tools build the same arg via withOrg in grafanabind.go.
func orgArg() mcp.ToolOption {
	return mcp.WithString("org", mcp.Required(), mcp.Description(orgArgDescription))
}

// maybeAddTool wraps s.AddTool with a --disabled-tools check. Mirrors
// mcp-grafana's maybeAddTools (cmd/mcp-grafana/main.go) but at
// tool-name granularity. nil disabled = no filter.
func maybeAddTool(s *mcpsrv.MCPServer, disabled map[string]bool, t mcp.Tool, h mcpsrv.ToolHandlerFunc) {
	if disabled[t.Name] {
		return
	}
	s.AddTool(t, h)
}

// RegisterAll wires every category of tool into the MCP server. See
// doc.go for the per-category breakdown. ctx is used only for the
// Tempo binder's one-shot startup discovery. disabled (typically
// sourced from --disabled-tools) is consulted at every s.AddTool site
// via maybeAddTool; nil = no filter.
func RegisterAll(ctx context.Context, s *mcpsrv.MCPServer, logger *slog.Logger, az authz.Authorizer, ol authz.OrgLister, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo, disabled map[string]bool) error {
	b, err := newGFBinder(az, gc, grafanaURL, apiKey, basicAuth, disabled)
	if err != nil {
		return err
	}
	registerOrgTools(s, disabled, az, b)
	registerDashboardTools(s, b)
	registerMetricsTools(s, b)
	registerLogTools(s, b)
	registerAlertingTools(s, b)
	registerAlertTools(s, disabled, az, gc)
	registerSilenceTools(s, disabled, az, gc)
	registerExampleTools(s, b)
	if err := registerTempoTools(ctx, s, logger, b, ol); err != nil {
		return err
	}
	return nil
}
