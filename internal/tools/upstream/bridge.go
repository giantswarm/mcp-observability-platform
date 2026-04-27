// Package upstream bridges this MCP's tool surface to upstream
// grafana/mcp-grafana tool handlers. The Bridge handles the parts that
// are local-specific (org → OrgID resolution via authz, X-Grafana-User
// caller attribution) and delegates the actual Grafana interaction to
// upstream's tools so we track upstream changes for free.
package upstream

import (
	"context"
	"maps"
	"net/url"

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// Bridge wraps upstream tool handlers with our org→OrgID authz and
// per-request Grafana context injection. Construct one at startup with
// the base Grafana URL + service-account token, then call Wrap to
// produce a server.ToolHandlerFunc for each upstream tool you want to
// register on the local MCP server.
//
// Per-call lifecycle:
//
//  1. Read "org" from the request, resolve it via Authorizer.RequireOrg
//     to a fully-populated authorized Organization at >= role.
//  2. Build an mcpgrafana.GrafanaConfig with our base URL + APIKey,
//     overlay the resolved OrgID + caller-derived X-Grafana-User
//     header, and stash it in ctx via WithGrafanaConfig.
//  3. Construct a fresh upstream GrafanaClient and stash it in ctx via
//     WithGrafanaClient. Per-request construction is required because
//     upstream's RoundTrippers freeze OrgID at construction time
//     (tracked upstream as grafana/mcp-grafana#794).
//  4. Invoke the upstream Tool.Handler. Upstream's argument schema is
//     left intact — local registrations only add an "org" arg on top,
//     which upstream's JSON unmarshaler ignores as an unknown field.
type Bridge struct {
	Authorizer authz.Authorizer
	GrafanaURL string
	// APIKey takes precedence when both are set; the loader rejects that
	// at startup (cmd/config.go) so in practice exactly one is non-zero.
	APIKey    string
	BasicAuth *url.Userinfo
}

// WithOrg returns a copy of an upstream tool definition with an "org"
// argument prepended to its input schema (string, required). Use it
// when registering a wrapped upstream tool on the local MCP server so
// the LLM-visible schema is upstream's verbatim plus our org parameter.
//
// The returned mcp.Tool shares no mutable state with the input — the
// Properties map and Required slice are deep-copied. Safe to call once
// per registration.
const orgArgDescription = "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs."

func WithOrg(t mcp.Tool) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	props["org"] = map[string]any{
		"type":        "string",
		"description": orgArgDescription,
	}
	out.InputSchema.Properties = props
	req := make([]string, 0, len(t.InputSchema.Required)+1)
	req = append(req, "org")
	req = append(req, t.InputSchema.Required...)
	out.InputSchema.Required = req
	return out
}

// Wrap returns a tool-handler that performs our authz + context setup,
// then delegates to upstream's tool handler. role is the minimum
// authz.Role required on the requested org; tools that traditionally
// gated on RoleViewer keep that boundary unchanged.
func (b *Bridge) Wrap(role authz.Role, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		org, err := b.Authorizer.RequireOrg(ctx, orgRef, role)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		cfg := mcpgrafana.GrafanaConfig{
			URL:       b.GrafanaURL,
			APIKey:    b.APIKey,
			BasicAuth: b.BasicAuth,
			OrgID:     org.OrgID,
			ExtraHeaders: map[string]string{
				// Grafana audit log attribution to the OIDC subject
				// rather than the server-admin SA we authenticate with.
				"X-Grafana-User": authz.CallerSubject(ctx),
			},
		}
		ctx = mcpgrafana.WithGrafanaConfig(ctx, cfg)
		ctx = mcpgrafana.WithGrafanaClient(ctx, mcpgrafana.NewGrafanaClient(ctx, b.GrafanaURL, b.APIKey, b.BasicAuth))
		return upstream.Handler(ctx, req)
	}
}
