// Package upstream registers tool handlers from grafana/mcp-grafana onto
// our MCP server, applying our org→OrgID authz + datasource UID
// resolution + X-Grafana-User caller attribution before delegating. The
// upstream library does the actual Grafana interaction so we track its
// changes for free.
package upstream

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
// "org" argument we prepend to every wrapped upstream tool.
const orgArgDescription = "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs."

// DatasourceUIDArg is upstream's conventional argument name for the
// datasource UID. Most upstream tools use this; alerting_manage_rules
// uses "datasource_uid" (snake_case) — pass that string explicitly.
const DatasourceUIDArg = "datasourceUid"

// Registrar registers upstream grafana/mcp-grafana tool handlers onto an
// MCP server, wrapping each one with our org→OrgID authz, per-request
// Grafana context injection, and (for datasource-scoped tools)
// datasourceUid resolution. Construct one at startup, then call Org or
// Datasource for each upstream tool you want to register.
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
type Registrar struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo
}

// NewRegistrar constructs a Registrar after validating its dependencies.
// APIKey and BasicAuth are mutually exclusive; exactly one must be set.
// Returns an error on misconfiguration so the failure is at startup.
func NewRegistrar(authorizer authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) (*Registrar, error) {
	if authorizer == nil {
		return nil, errors.New("upstream: Authorizer is required")
	}
	if gc == nil {
		return nil, errors.New("upstream: Grafana client is required")
	}
	if grafanaURL == "" {
		return nil, errors.New("upstream: Grafana URL is required")
	}
	hasKey := apiKey != ""
	hasBasic := basicAuth != nil
	if hasKey == hasBasic {
		return nil, errors.New("upstream: exactly one of APIKey or BasicAuth must be set")
	}
	return &Registrar{
		authorizer: authorizer,
		grafana:    gc,
		url:        grafanaURL,
		apiKey:     apiKey,
		basicAuth:  basicAuth,
	}, nil
}

// Org registers an upstream tool that needs only org→OrgID resolution.
// The synthetic "org" argument is prepended to the LLM-visible schema;
// every other arg passes through unchanged.
func (r *Registrar) Org(s *server.MCPServer, role authz.Role, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, ""), r.wrap(role, "", "", t))
}

// Datasource registers an upstream tool that needs a datasource UID.
// Replaces upstream's argName in the schema with our "org"; resolves the
// org's datasource of kind, looks its UID up via Grafana, and injects
// argName=<uid> server-side so the LLM never sees a datasourceUid arg.
//
// Pass DatasourceUIDArg ("datasourceUid") for the typical case; pass
// "datasource_uid" (snake_case) for alerting_manage_rules.
func (r *Registrar) Datasource(s *server.MCPServer, role authz.Role, kind authz.DatasourceKind, argName string, t mcpgrafana.Tool) {
	s.AddTool(withOrg(t.Tool, argName), r.wrap(role, kind, argName, t))
}

// withOrg returns a copy of an upstream tool definition with an "org"
// argument prepended (string, required). When replaceArg is non-empty,
// that argument is removed from the LLM-visible schema (Properties +
// Required) — used for the datasource-uid arg the registrar fills
// server-side.
//
// Properties map and Required slice are deep-copied; the input is never
// mutated. Panics if the upstream tool already declares "org".
func withOrg(t mcp.Tool, replaceArg string) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	if replaceArg != "" {
		delete(props, replaceArg)
	}
	if _, collides := props["org"]; collides {
		panic(fmt.Sprintf("upstream: tool %q already declares an 'org' argument; registrar cannot add its own", t.Name))
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
func (r *Registrar) wrap(role authz.Role, kind authz.DatasourceKind, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("missing arg", err), nil
		}
		org, err := r.authorizer.RequireOrg(ctx, orgRef, role)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("authz", err), nil
		}
		if kind != "" {
			ds, ok := org.FindDatasource(kind)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("org %q has no %s datasource configured", orgRef, kind)), nil
			}
			// Round-trip ID→UID via Grafana — upstream tools take the
			// UID, our CR carries the ID. See docs/roadmap.md
			// (uid-publish) for the path to dropping this lookup.
			uid, err := r.grafana.LookupDatasourceUIDByID(ctx, grafana.RequestOpts{OrgID: org.OrgID, Caller: authz.CallerSubject(ctx)}, ds.ID)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("datasource lookup", err), nil
			}
			injectArg(&req, argName, uid)
		}
		return upstream.Handler(r.attachGrafana(ctx, org.OrgID), req)
	}
}

// attachGrafana stashes our GrafanaConfig and a fresh upstream
// GrafanaClient on ctx for upstream's handler to pick up. Per-request
// client construction works around upstream's OrgID-frozen RoundTripper
// (grafana/mcp-grafana#794).
func (r *Registrar) attachGrafana(ctx context.Context, orgID int64) context.Context {
	cfg := mcpgrafana.GrafanaConfig{
		URL:       r.url,
		APIKey:    r.apiKey,
		BasicAuth: r.basicAuth,
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
	ctx = mcpgrafana.WithGrafanaClient(ctx, mcpgrafana.NewGrafanaClient(ctx, r.url, r.apiKey, r.basicAuth))
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
