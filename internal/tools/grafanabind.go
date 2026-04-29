package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sync"

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// orgArgDescription is the LLM-visible description of the synthetic
// "org" argument we prepend to every wrapped upstream tool. Shared with
// local tools (alerts, traces) via orgArg().
const orgArgDescription = "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs."

// datasourceUIDArg is upstream's conventional argument name for the
// datasource UID. Most upstream tools use this; alerting_manage_rules
// uses "datasource_uid" (snake_case) — pass that string explicitly.
const datasourceUIDArg = "datasourceUid"

// gfBinder wraps grafana/mcp-grafana tool handlers with our org→OrgID
// authz, per-request Grafana context injection, and (for
// datasource-scoped tools) datasource UID resolution.
//
// One mcp-grafana GrafanaClient is cached per (resolved) OrgID. Two
// reasons for that, both in mcpgrafana.OrgIDRoundTripper:
//   - On HTTP-style calls the round-tripper reads OrgID from
//     GrafanaConfigFromContext(req.Context()) and overrides per-request.
//   - On openapi-runtime calls (e.g. c.Datasources.GetDataSources()),
//     params.Context is nil and the runtime falls back to
//     context.Background(), so the override branch never fires — only
//     the orgID baked at construction (t.orgID) reaches the wire.
//
// Caching by OrgID makes the second path correct: every per-org client
// has the right t.orgID baked in, so /api/datasources answers for the
// caller's org rather than the SA's home org. Cache is unbounded, but
// |orgs| is small (per Grafana install).
type gfBinder struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo

	clientsMu sync.Mutex
	clients   map[int64]*mcpgrafana.GrafanaClient // key: OrgID
}

// newGFBinder constructs a gfBinder after validating its dependencies.
// APIKey/BasicAuth mutual-exclusivity is enforced upstream at config load
// (cmd/config.go) and at grafana.New — not re-checked here.
func newGFBinder(authorizer authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) (*gfBinder, error) {
	if authorizer == nil {
		return nil, errors.New("authorizer is required")
	}
	if gc == nil {
		return nil, errors.New("grafana client is required")
	}
	if grafanaURL == "" {
		return nil, errors.New("grafana URL is required")
	}
	return &gfBinder{
		authorizer: authorizer,
		grafana:    gc,
		url:        grafanaURL,
		apiKey:     apiKey,
		basicAuth:  basicAuth,
		clients:    make(map[int64]*mcpgrafana.GrafanaClient),
	}, nil
}

// clientFor returns the GrafanaClient with t.orgID == orgID baked in,
// creating + caching one on miss. The /api/frontend/settings probe
// inside NewGrafanaClient runs once per (new) OrgID.
func (b *gfBinder) clientFor(orgID int64) *mcpgrafana.GrafanaClient {
	b.clientsMu.Lock()
	defer b.clientsMu.Unlock()
	if c, ok := b.clients[orgID]; ok {
		return c
	}
	cfg := mcpgrafana.GrafanaConfig{URL: b.url, APIKey: b.apiKey, BasicAuth: b.basicAuth, OrgID: orgID}
	c := mcpgrafana.NewGrafanaClient(mcpgrafana.WithGrafanaConfig(context.Background(), cfg), b.url, b.apiKey, b.basicAuth)
	b.clients[orgID] = c
	return c
}

// bindOrgTool registers an upstream tool that needs only org→OrgID
// resolution. The synthetic "org" argument is prepended to the
// LLM-visible schema; every other arg passes through unchanged.
func (b *gfBinder) bindOrgTool(s *server.MCPServer, role authz.Role, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, ""), b.wrap(role, "", "", "", t))
}

// bindDatasourceTool registers an upstream tool that needs a datasource
// UID. Replaces upstream's argName in the schema with our "org";
// resolves the org's datasource of kind, looks its UID up via Grafana,
// and injects argName=<uid> server-side so the LLM never sees a
// datasourceUid arg.
//
// tenantType gates the call to orgs that carry that tenant type
// (TenantTypeData for metrics/logs/traces/rules; TenantTypeAlerting is
// reserved for Alertmanager-shaped tools, which today are local).
//
// Pass datasourceUIDArg ("datasourceUid") for the typical case; pass
// "datasource_uid" (snake_case) for alerting_manage_rules.
func (b *gfBinder) bindDatasourceTool(s *server.MCPServer, role authz.Role, tenantType authz.TenantType, kind grafana.DatasourceKind, argName string, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, argName), b.wrap(role, tenantType, kind, argName, t))
}

// withOrg returns a copy of an upstream tool definition with an "org"
// argument prepended (string, required). When replaceArg is non-empty,
// that argument is removed from the LLM-visible schema (Properties +
// Required) — used for the datasource-uid arg the binder fills
// server-side.
//
// Properties map and Required slice are deep-copied; the input is never
// mutated. Panic at registration, not per-request — every wrapped tool
// is enumerated by RegisterAll, so a collision shows up at process start.
func withOrg(t mcp.Tool, replaceArg string) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	if replaceArg != "" {
		delete(props, replaceArg)
	}
	if _, collides := props["org"]; collides {
		panic(fmt.Sprintf("tools: tool %q already declares an 'org' argument; binder cannot add its own", t.Name))
	}
	props["org"] = map[string]any{
		"type":        "string",
		"description": orgArgDescription,
	}
	out.InputSchema.Properties = props

	req := make([]string, 0, len(t.InputSchema.Required)+1)
	req = append(req, "org")
	for _, r := range t.InputSchema.Required {
		if r == replaceArg {
			continue
		}
		req = append(req, r)
	}
	out.InputSchema.Required = req
	return out
}

// wrap builds the tool-handler that performs authz + context setup, then
// delegates to upstream's tool handler. kind == "" skips the datasource
// resolution and tenant-type checks (org-only path); kind != "" requires
// argName to be the schema arg the upstream handler reads the UID from
// and a tenantType the org must carry.
func (b *gfBinder) wrap(role authz.Role, tenantType authz.TenantType, kind grafana.DatasourceKind, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("missing arg", err), nil
		}
		org, err := b.authorizer.RequireOrg(ctx, orgRef, role)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("authz", err), nil
		}
		if kind != "" {
			if tenantType != "" && !org.HasTenantType(tenantType) {
				return mcp.NewToolResultError(fmt.Sprintf("org %q has no tenant of type %q — tool unavailable", orgRef, tenantType)), nil
			}
			ds, ok := org.FindDatasource(kind)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("org %q has no %s datasource configured", orgRef, kind)), nil
			}
			uid, err := b.grafana.LookupDatasourceUIDByID(ctx, grafana.RequestOpts{OrgID: org.OrgID, Caller: authz.CallerSubject(ctx)}, ds.ID)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("datasource lookup", err), nil
			}
			if err := injectArg(&req, argName, uid); err != nil {
				return mcp.NewToolResultErrorFromErr("malformed arguments", err), nil
			}
		}
		return upstream.Handler(b.attachGrafana(ctx, org.OrgID), req)
	}
}

// attachGrafana stashes a per-request GrafanaConfig and the per-OrgID
// GrafanaClient on ctx. The HTTP-style mcp-grafana round-trippers read
// OrgID/ExtraHeaders from the config; the openapi-runtime path can't
// (its req.Context() is background), but the per-OrgID client baked
// t.orgID at construction so the right header still ships.
func (b *gfBinder) attachGrafana(ctx context.Context, orgID int64) context.Context {
	cfg := mcpgrafana.GrafanaConfig{
		URL:       b.url,
		APIKey:    b.apiKey,
		BasicAuth: b.basicAuth,
		OrgID:     orgID,
	}
	if subj := authz.CallerSubject(ctx); subj != "" {
		// Grafana audit-log attribution to the OIDC subject rather
		// than the server-admin SA we authenticate with.
		cfg.ExtraHeaders = map[string]string{"X-Grafana-User": subj}
	}
	ctx = mcpgrafana.WithGrafanaConfig(ctx, cfg)
	ctx = mcpgrafana.WithGrafanaClient(ctx, b.clientFor(orgID))
	return ctx
}

// injectArg sets a key on req.Params.Arguments, copy-on-write. Returns
// an error rather than silently dropping the caller's args when the
// shape is malformed (json.RawMessage that doesn't decode as an object,
// or any other unexpected type).
func injectArg(req *mcp.CallToolRequest, key string, value any) error {
	switch a := req.Params.Arguments.(type) {
	case nil:
		req.Params.Arguments = map[string]any{key: value}
	case map[string]any:
		next := make(map[string]any, len(a)+1)
		maps.Copy(next, a)
		next[key] = value
		req.Params.Arguments = next
	case json.RawMessage:
		next := map[string]any{}
		if err := json.Unmarshal(a, &next); err != nil {
			return fmt.Errorf("decode arguments: %w", err)
		}
		next[key] = value
		req.Params.Arguments = next
	default:
		return fmt.Errorf("unexpected arguments type %T", a)
	}
	return nil
}
