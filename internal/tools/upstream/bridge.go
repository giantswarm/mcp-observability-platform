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

// orgArgDescription is the LLM-visible description of the synthetic "org"
// argument we prepend to every wrapped upstream tool. Single source so
// the wording stays consistent across WithOrg / WithOrgReplacingDatasource.
const orgArgDescription = "Organization — either the GrafanaOrganization CR name or its display name. See list_orgs."

// Bridge wraps upstream tool handlers with our org → OrgID authz,
// per-request Grafana context injection, and (for datasource-scoped
// tools) datasourceUid resolution. Construct one with NewBridge at
// startup, then call Wrap or WrapDatasource for each upstream tool you
// want to register on the local MCP server.
//
// Per-call lifecycle (Wrap):
//
//  1. Read "org" from the request, resolve via Authorizer.RequireOrg to
//     a fully-populated authorised Organization at >= role.
//  2. Build mcpgrafana.GrafanaConfig with our base URL + APIKey, overlay
//     the resolved OrgID + caller-derived X-Grafana-User header (skipped
//     when caller subject is empty), and stash it in ctx via
//     WithGrafanaConfig.
//  3. Construct a fresh upstream GrafanaClient and stash it in ctx via
//     WithGrafanaClient. Per-request construction is required because
//     upstream's RoundTrippers freeze OrgID at construction time
//     (tracked upstream as grafana/mcp-grafana#794).
//  4. Invoke the upstream Tool.Handler.
//
// WrapDatasource adds a step 1.5: pick the Datasource on the org by
// kind, look up its UID via Grafana, and inject "datasourceUid" into
// the request before delegation. Use WithOrgReplacingDatasource on the
// schema side so the LLM doesn't see the upstream "datasourceUid" arg
// (we fill it server-side).
type Bridge struct {
	authorizer authz.Authorizer
	grafana    grafana.Client
	url        string
	apiKey     string
	basicAuth  *url.Userinfo
}

// NewBridge constructs a Bridge after validating its dependencies. APIKey
// and BasicAuth are mutually exclusive; exactly one must be set. Returns
// an error on misconfiguration so the failure is at startup, not on the
// first tool call.
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
// argument prepended to its input schema (string, required). The
// returned mcp.Tool shares no mutable state with the input — Properties
// map and Required slice are deep-copied. Safe to call once per
// registration.
func WithOrg(t mcp.Tool) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	if _, collides := props["org"]; collides {
		// Defensive: an upstream tool that already names "org" would have
		// our value silently overwrite theirs. Today no upstream tool
		// does this, but a future upstream release would and the silent
		// collision would be hard to debug. Panic at registration —
		// fail loud at startup.
		panic(fmt.Sprintf("upstream: tool %q already declares an 'org' argument; bridge cannot add its own", t.Name))
	}
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

// WithOrgReplacingArg is the schema variant for bridged tools whose
// datasource-uid arg is filled by the bridge (see WrapDatasource). Adds
// an "org" argument (required, first) and REMOVES the named arg from
// the LLM-visible schema entirely — Properties and Required — since
// the LLM no longer needs to know about it.
//
// Most upstream tools use "datasourceUid" (camelCase); some (e.g.
// alerting_manage_rules) use "datasource_uid" (snake_case) instead.
// argName is the upstream arg key to remove.
//
// Same deep-copy discipline as WithOrg.
func WithOrgReplacingArg(t mcp.Tool, argName string) mcp.Tool {
	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	delete(props, argName)
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
		if r == argName {
			continue
		}
		req = append(req, r)
	}
	out.InputSchema.Required = req
	return out
}

// WithOrgReplacingDatasource is the common case: replaces the upstream
// "datasourceUid" arg with our server-side injection.
func WithOrgReplacingDatasource(t mcp.Tool) mcp.Tool {
	return WithOrgReplacingArg(t, "datasourceUid")
}

// Wrap returns a tool-handler that performs our authz + context setup,
// then delegates to upstream's tool handler. role is the minimum
// authz.Role required on the requested org.
func (b *Bridge) Wrap(role authz.Role, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("missing arg", err), nil
		}
		org, err := b.authorizer.RequireOrg(ctx, orgRef, role)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("authz", err), nil
		}
		return upstream.Handler(b.attachGrafana(ctx, org.OrgID), req)
	}
}

// WrapDatasourceArg is the parametrized version of WrapDatasource for
// upstream tools whose datasource-uid argument has a non-default name
// (e.g. alerting_manage_rules uses "datasource_uid", not "datasourceUid").
//
// Pair with WithOrgReplacingArg(t, argName) on the schema side.
func (b *Bridge) WrapDatasourceArg(role authz.Role, kind authz.DatasourceKind, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgRef, err := req.RequireString("org")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("missing arg", err), nil
		}
		org, err := b.authorizer.RequireOrg(ctx, orgRef, role)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("authz", err), nil
		}
		ds, ok := org.FindDatasource(kind)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("org %q has no %s datasource configured", orgRef, kind)), nil
		}
		// TODO(uid-publish): once observability-operator publishes
		// datasource UIDs in GrafanaOrganization.status.datasources[].uid,
		// drop this lookup and read uid from ds directly.
		uid, err := b.grafana.LookupDatasourceUIDByID(ctx, grafana.RequestOpts{OrgID: org.OrgID, Caller: authz.CallerSubject(ctx)}, ds.ID)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("datasource lookup", err), nil
		}
		injectArg(&req, argName, uid)
		return upstream.Handler(b.attachGrafana(ctx, org.OrgID), req)
	}
}

// WrapDatasource is the common case: upstream tools that read the
// datasource UID from a "datasourceUid" arg. Equivalent to
// WrapDatasourceArg(role, kind, "datasourceUid", upstream).
//
// Pair with WithOrgReplacingDatasource on the schema side.
func (b *Bridge) WrapDatasource(role authz.Role, kind authz.DatasourceKind, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return b.WrapDatasourceArg(role, kind, "datasourceUid", upstream)
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

// injectArg sets a key on req.Params.Arguments, copy-on-write. The map
// is reallocated rather than mutated in place — request arguments are
// not ours to scribble on, even though mcp-go would tolerate it for a
// single-handler call. Handles the three shapes Arguments can carry:
// nil (no args at all), map[string]any (the common case), and
// json.RawMessage (some transports unmarshal lazily).
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
		// Unknown shape — fall back to a fresh map carrying just our
		// injection. Caller args are lost, but this branch isn't reached
		// in practice (mcp-go always hands either nil or map[string]any).
		req.Params.Arguments = map[string]any{key: value}
	}
}

