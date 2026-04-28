// Package upstream bridges this MCP's tool surface to upstream
// grafana/mcp-grafana tool handlers. The Bridge handles the parts that
// are local-specific (org → OrgID resolution via authz, X-Grafana-User
// caller attribution, datasource UID injection) and delegates the
// actual Grafana interaction to upstream's tools so we track upstream
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

// Bridge wraps upstream tool handlers with our org → OrgID authz,
// per-request Grafana context injection, and (for datasource-scoped
// tools) datasourceUid resolution. Construct one with NewBridge at
// startup, then call Wrap for each upstream tool you want to register.
//
// Per-call lifecycle:
//
//  1. Read "org" from the request, resolve via Authorizer.RequireOrg
//     to a fully-populated authorised Organization at >= role.
//  2. (Datasource tools, kind != "") pick the Datasource on the org by
//     kind, look up its UID via Grafana, and inject argName=uid into
//     the request before delegation.
//  3. Build mcpgrafana.GrafanaConfig with our base URL + APIKey,
//     overlay the resolved OrgID + caller-derived X-Grafana-User
//     header (skipped when caller subject is empty), and stash it in
//     ctx via WithGrafanaConfig.
//  4. Construct a fresh upstream GrafanaClient and stash it in ctx.
//     Per-request construction is required because upstream's
//     RoundTrippers freeze OrgID at construction time
//     (grafana/mcp-grafana#794).
//  5. Invoke the upstream Tool.Handler.
type Bridge struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo
}

// NewBridge constructs a Bridge after validating its dependencies.
// APIKey and BasicAuth are mutually exclusive; exactly one must be set.
// Returns an error on misconfiguration so the failure is at startup.
func NewBridge(authorizer authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo) (*Bridge, error) {
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
	return &Bridge{
		authorizer: authorizer,
		grafana:    gc,
		url:        grafanaURL,
		apiKey:     apiKey,
		basicAuth:  basicAuth,
	}, nil
}

// WithOrg returns a copy of an upstream tool definition with an "org"
// argument prepended (string, required). When replaceArg is non-empty,
// that argument is removed from the LLM-visible schema (Properties +
// Required) — used for the datasource-uid arg that the bridge fills
// server-side.
//
// Properties map and Required slice are deep-copied; the input is
// never mutated. Panics if the upstream tool already declares "org".
func WithOrg(t mcp.Tool, replaceArg string) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	if replaceArg != "" {
		delete(props, replaceArg)
	}
	if _, collides := props["org"]; collides {
		panic(fmt.Sprintf("upstream: tool %q already declares an 'org' argument; bridge cannot add its own", t.Name))
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

// Wrap returns a tool-handler that performs our authz + context setup,
// then delegates to upstream's tool handler. role is the minimum
// authz.Role required on the requested org.
//
// When kind != "", the handler also resolves the datasource of that
// kind on the org, looks up its UID via Grafana, and injects argName=uid
// into the request. argName is conventionally DatasourceUIDArg
// ("datasourceUid"); pair with WithOrg(t, argName) on the schema side.
//
// When kind == "", argName is ignored — pure org-scoped tools that
// don't need a datasource (dashboards, list_datasources, etc.). Pair
// with WithOrg(t, "") on the schema side.
func (b *Bridge) Wrap(role authz.Role, kind authz.DatasourceKind, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
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
			// Round-trip ID→UID via Grafana — upstream tools take the
			// UID, our CR carries the ID. See docs/roadmap.md
			// (uid-publish) for the path to dropping this lookup.
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
// construction works around upstream's OrgID-frozen RoundTripper
// (grafana/mcp-grafana#794).
func (b *Bridge) attachGrafana(ctx context.Context, orgID int64) context.Context {
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
