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
type gfBinder struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo

	// upstream is a single client reused across requests. mcp-grafana
	// v0.12.1 (#805) made the OrgID/ExtraHeaders/Auth RoundTrippers read
	// from GrafanaConfigFromContext(req.Context()) on every call, so per
	// caller state moves to ctx via attachGrafana. The client is built
	// lazily on first authz-passed request because NewGrafanaClient
	// hits /api/frontend/settings during construction.
	upstreamOnce sync.Once
	upstream     *mcpgrafana.GrafanaClient
}

// newGFBinder constructs a gfBinder after validating its dependencies.
// APIKey and BasicAuth are mutually exclusive; exactly one must be set.
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
	hasKey := apiKey != ""
	hasBasic := basicAuth != nil
	if hasKey == hasBasic {
		return nil, errors.New("exactly one of APIKey or BasicAuth must be set")
	}
	return &gfBinder{
		authorizer: authorizer,
		grafana:    gc,
		url:        grafanaURL,
		apiKey:     apiKey,
		basicAuth:  basicAuth,
	}, nil
}

// upstreamClient lazily builds the shared upstream GrafanaClient on
// first use so the /api/frontend/settings round-trip in
// NewGrafanaClient is deferred until an authz-passing request runs.
func (b *gfBinder) upstreamClient(ctx context.Context) *mcpgrafana.GrafanaClient {
	b.upstreamOnce.Do(func() {
		b.upstream = mcpgrafana.NewGrafanaClient(ctx, b.url, b.apiKey, b.basicAuth)
	})
	return b.upstream
}

// bindOrgTool registers an upstream tool that needs only org→OrgID
// resolution. The synthetic "org" argument is prepended to the
// LLM-visible schema; every other arg passes through unchanged.
func (b *gfBinder) bindOrgTool(s *server.MCPServer, role authz.Role, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, ""), b.wrap(role, "", "", t))
}

// bindDatasourceTool registers an upstream tool that needs a datasource
// UID. Replaces upstream's argName in the schema with our "org";
// resolves the org's datasource of kind, looks its UID up via Grafana,
// and injects argName=<uid> server-side so the LLM never sees a
// datasourceUid arg.
//
// Pass datasourceUIDArg ("datasourceUid") for the typical case; pass
// "datasource_uid" (snake_case) for alerting_manage_rules.
func (b *gfBinder) bindDatasourceTool(s *server.MCPServer, role authz.Role, kind grafana.DatasourceKind, argName string, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, argName), b.wrap(role, kind, argName, t))
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
// resolution branch; kind != "" requires argName to be the schema arg
// the upstream handler reads the UID from.
func (b *gfBinder) wrap(role authz.Role, kind grafana.DatasourceKind, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
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

// attachGrafana stashes our GrafanaConfig and the shared upstream
// GrafanaClient on ctx. mcp-grafana v0.12.1's RoundTrippers read OrgID
// and ExtraHeaders from this config per-request, so the same client is
// safe to share across orgs.
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
	ctx = mcpgrafana.WithGrafanaClient(ctx, b.upstreamClient(ctx))
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
