package tools

import (
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// readOnlyToolAnnotation is the annotation of every local tool: it writes
// nothing, so it is also idempotent (a repeated call changes nothing more,
// though live data may differ), and open-world.
func readOnlyAnnotation() *mcp.ToolAnnotations {
	destructive, openWorld := false, true
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld, DestructiveHint: &destructive, IdempotentHint: true}
}

// Registrar accepts native SDK tools and handlers.
type Registrar interface {
	AddTool(*mcp.Tool, mcp.ToolHandler)
}

// maybeAddTool wraps s.AddTool with a --disabled-tools check. Mirrors
// mcp-grafana's maybeAddTools (cmd/mcp-grafana/main.go) but at
// tool-name granularity. nil disabled = no filter.
func maybeAddTool(s Registrar, disabled map[string]bool, t *mcp.Tool, h mcp.ToolHandler) {
	if disabled[t.Name] {
		return
	}
	s.AddTool(t, h)
}

type GrafanaAuth struct {
	APIKey    string
	BasicAuth *url.Userinfo
	JWTHeader string
}

// RegisterAll wires every category of tool into the MCP server. See
// doc.go for the per-category breakdown. disabled (typically
// sourced from --disabled-tools) is consulted at every s.AddTool site
// via maybeAddTool; nil = no filter.
func RegisterAll(s Registrar, az authz.Authorizer, gc grafana.Client, grafanaURL, grafanaPublicURL string, auth GrafanaAuth, disabled map[string]bool) error {
	b, err := newGFBinder(az, gc, grafanaURL, auth, disabled)
	if err != nil {
		return err
	}
	b.publicURL = grafanaPublicURL
	registerAll(s, az, gc, b, disabled)
	return nil
}

func registerAll(s Registrar, az authz.Authorizer, gc grafana.Client, b *gfBinder, disabled map[string]bool) {
	registerOrgTools(s, disabled, az, b)
	registerDashboardTools(s, b)
	registerMetricsTools(s, b)
	registerLogTools(s, b)
	registerAlertingTools(s, b)
	registerAlertTools(s, disabled, az, gc)
	registerSilenceTools(s, disabled, az, gc)
	registerExampleTools(s, b)
	registerTempoTools(s, b)
}
