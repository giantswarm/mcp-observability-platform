package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"

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
// Per-call lifecycle of a wrapped handler:
//
//  1. Read "org" from the request, resolve via Authorizer.RequireOrg to
//     a fully-populated authorised Organization at >= role.
//  2. (Datasource only) pick the Datasource on the org by kind, look up
//     its UID via Grafana, and inject argName=uid into the request
//     before delegation.
//  3. Build mcpgrafana.GrafanaConfig with our base URL + APIKey, overlay
//     the resolved OrgID + caller-derived X-Grafana-User header (skipped
//     when caller subject is empty), and stash it in ctx via
//     WithGrafanaConfig.
//  4. Construct a fresh upstream GrafanaClient and stash it in ctx.
//     Per-request construction works around upstream's RoundTrippers
//     freezing OrgID at construction time (grafana/mcp-grafana#794).
//  5. Invoke the upstream Tool.Handler.
type gfBinder struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo
}

// newGFBinder constructs a gfBinder after validating its dependencies.
// APIKey and BasicAuth are mutually exclusive; exactly one must be set.
func newGFBinder(authorizer authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) (*gfBinder, error) {
	if authorizer == nil {
		return nil, errors.New("Authorizer is required")
	}
	if gc == nil {
		return nil, errors.New("Grafana client is required")
	}
	if grafanaURL == "" {
		return nil, errors.New("Grafana URL is required")
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
// mutated.
//
// Panic at registration, not per-request — every wrapped tool is
// enumerated by RegisterAll, so a collision shows up at process start.
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
			injectArg(&req, argName, uid)
		}
		return upstream.Handler(b.attachGrafana(ctx, org.OrgID), req)
	}
}

// attachGrafana stashes our GrafanaConfig and a fresh upstream
// GrafanaClient on ctx for upstream's handler to pick up. Per-request
// client construction works around upstream's OrgID-frozen RoundTripper
// (grafana/mcp-grafana#794).
func (b *gfBinder) attachGrafana(ctx context.Context, orgID int64) context.Context {
	cfg := mcpgrafana.GrafanaConfig{
		URL:       b.url,
		APIKey:    b.apiKey,
		BasicAuth: b.basicAuth,
		OrgID:     orgID,
	}
	if subj := authz.CallerSubject(ctx); subj != "" {
		cfg.ExtraHeaders = map[string]string{
			// Grafana audit-log attribution to the OIDC subject rather
			// than the server-admin SA we authenticate with.
			"X-Grafana-User": subj,
		}
	}
	ctx = mcpgrafana.WithGrafanaConfig(ctx, cfg)
	ctx = mcpgrafana.WithGrafanaClient(ctx, mcpgrafana.NewGrafanaClient(ctx, b.url, b.apiKey, b.basicAuth))
	return ctx
}

// injectArg sets a key on req.Params.Arguments, copy-on-write. Handles
// the three shapes Arguments can carry: nil, map[string]any (common
// case), and json.RawMessage (some transports unmarshal lazily).
func injectArg(req *mcp.CallToolRequest, key string, value any) {
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
		_ = json.Unmarshal(a, &next)
		next[key] = value
		req.Params.Arguments = next
	default:
		req.Params.Arguments = map[string]any{key: value}
	}
}
