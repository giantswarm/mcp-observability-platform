package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
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
// uses datasourceUIDArgSnake.
const (
	datasourceUIDArg      = "datasourceUid"
	datasourceUIDArgSnake = "datasource_uid"
)

// datasourceUIDHint is appended to the LLM-visible description of every
// datasource-UID argument the binder demotes to optional. The hint
// teaches the LLM the operator naming convention so it can resolve
// "the giantswarm tenant" → gs-{kind}-giantswarm via list_datasources
// without us baking name-parsing into server code.
const datasourceUIDHint = "Optional: override the default datasource — pass this when the user asks about a specific tenant. " +
	"Operator-managed datasources follow `gs-{kind}-{tenant}` (mono-tenant); the unsuffixed `gs-{kind}` is the multi-tenant aggregate. " +
	"Call list_datasources to discover available UIDs for the org."

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
	disabled   map[string]bool // --disabled-tools lookup; nil = no filter

	clientsMu sync.Mutex
	clients   map[int64]*mcpgrafana.GrafanaClient // key: OrgID
}

// newGFBinder constructs a gfBinder after validating its dependencies.
// APIKey/BasicAuth mutual-exclusivity is enforced upstream at config load
// (cmd/config.go) and at grafana.New — not re-checked here. disabled may
// be nil; bind* methods route s.AddTool through maybeAddTool, which is
// nil-safe.
func newGFBinder(authorizer authz.Authorizer, gc grafana.Client, grafanaURL, apiKey string, basicAuth *url.Userinfo, disabled map[string]bool) (*gfBinder, error) {
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
		disabled:   disabled,
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
	maybeAddTool(s, b.disabled, withOrg(t.Tool, ""), b.wrap(role, "", "", "", t))
}

// bindDatasourceTool registers an upstream tool that needs a datasource
// UID. The schema retains argName as an optional override; absent the
// override, the binder picks the first live datasource matching dsType
// and injects its UID server-side.
//
// tenantType gates the call to orgs that carry that tenant type
// (TenantTypeData for metrics/logs/traces/rules; TenantTypeAlerting is
// reserved for Alertmanager-shaped tools, which today are local).
//
// Pass datasourceUIDArg ("datasourceUid") for the typical case; pass
// "datasource_uid" (snake_case) for alerting_manage_rules.
func (b *gfBinder) bindDatasourceTool(s *server.MCPServer, role authz.Role, tenantType authz.TenantType, dsType grafana.DatasourceType, argName string, t mcpgrafana.Tool) {
	maybeAddTool(s, b.disabled, withOrg(t.Tool, argName), b.wrap(role, tenantType, dsType, argName, t))
}

// bindDatasourceFanoutTool registers a read-only upstream tool that
// runs once per ruler-capable datasource the org has, merging the
// per-DS results. Caller-supplied argName (e.g. "datasource_uid")
// short-circuits to a single upstream call.
func (b *gfBinder) bindDatasourceFanoutTool(s *server.MCPServer, role authz.Role, tenantType authz.TenantType, argName string, t mcpgrafana.Tool) {
	maybeAddTool(s, b.disabled, withOrg(t.Tool, ""), b.wrapFanout(role, tenantType, argName, t))
}

func (b *gfBinder) wrapFanout(role authz.Role, tenantType authz.TenantType, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, errRes := b.requireOrg(ctx, req, role, tenantType, true)
		if errRes != nil {
			return errRes, nil
		}
		ctx = b.attachGrafana(ctx, org.OrgID)

		if uid := req.GetString(argName, ""); uid != "" {
			return upstream.Handler(ctx, req)
		}

		opts := grafana.RequestOpts{OrgID: org.OrgID, Caller: authz.CallerSubject(ctx)}
		dss, err := b.grafana.ListDatasources(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("list datasources", err), nil
		}

		type entry struct {
			Name  string          `json:"name"`
			UID   string          `json:"uid"`
			Type  string          `json:"type"`
			Rules json.RawMessage `json:"rules,omitempty"`
			Error string          `json:"error,omitempty"`
		}
		entries := make([]entry, 0, len(dss))
		for _, ds := range dss {
			if !ds.ManageAlerts || !isRulerType(ds.Type) {
				continue
			}
			sub := req
			if err := injectArg(&sub, argName, ds.UID); err != nil {
				entries = append(entries, entry{Name: ds.Name, UID: ds.UID, Type: ds.Type, Error: err.Error()})
				continue
			}
			res, err := upstream.Handler(ctx, sub)
			switch {
			case err != nil:
				entries = append(entries, entry{Name: ds.Name, UID: ds.UID, Type: ds.Type, Error: err.Error()})
			case res != nil && res.IsError:
				entries = append(entries, entry{Name: ds.Name, UID: ds.UID, Type: ds.Type, Error: textOf(res)})
			default:
				entries = append(entries, entry{Name: ds.Name, UID: ds.UID, Type: ds.Type, Rules: rulesPayload(res)})
			}
		}
		return mcp.NewToolResultJSON(map[string]any{"datasources": entries})
	}
}

// isRulerType mirrors mcp-grafana's isRulerDatasource: only datasources
// whose plugin type contains DSTypePrometheus or DSTypeLoki expose a
// Cortex-style ruler API. Mimir registers as plugin type "prometheus".
func isRulerType(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, string(grafana.DSTypePrometheus)) || strings.Contains(t, string(grafana.DSTypeLoki))
}

func textOf(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// rulesPayload extracts upstream's text content. Embedded as raw JSON
// when valid; falls back to a JSON-encoded string so the merged result
// is always well-formed.
func rulesPayload(r *mcp.CallToolResult) json.RawMessage {
	s := textOf(r)
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	enc, _ := json.Marshal(s)
	return enc
}

// withOrg returns a copy of an upstream tool definition with an "org"
// argument prepended (string, required). When demoteArg is non-empty,
// that argument is kept in the schema but moved out of Required and
// its description gets datasourceUIDHint appended — the LLM sees it as
// an optional override and the binder fills it server-side when
// omitted.
//
// Properties map and Required slice are deep-copied; the input is never
// mutated. Panic at registration, not per-request — every wrapped tool
// is enumerated by RegisterAll, so a collision shows up at process start.
//
// Upstream mcp-grafana tools are produced by MustTool, which stores the
// schema as RawInputSchema (json.RawMessage). mcp.Tool.MarshalJSON emits
// RawInputSchema verbatim and ignores the structured InputSchema, so we
// normalize raw→structured up front. The function body then operates on
// a single representation and the tool ships with our org arg visible.
func withOrg(t mcp.Tool, demoteArg string) mcp.Tool {
	if len(t.RawInputSchema) > 0 {
		var raw struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(t.RawInputSchema, &raw); err != nil {
			panic(fmt.Sprintf("tools: tool %q has invalid RawInputSchema: %v", t.Name, err))
		}
		t.InputSchema.Type = "object"
		t.InputSchema.Properties = raw.Properties
		t.InputSchema.Required = raw.Required
		t.RawInputSchema = nil
	}

	out := t
	props := make(map[string]any, len(t.InputSchema.Properties)+1)
	maps.Copy(props, t.InputSchema.Properties)
	if demoteArg != "" {
		if cur, ok := props[demoteArg].(map[string]any); ok {
			merged := make(map[string]any, len(cur))
			maps.Copy(merged, cur)
			desc, _ := merged["description"].(string)
			merged["description"] = strings.TrimSpace(desc + " " + datasourceUIDHint)
			props[demoteArg] = merged
		}
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
		if r == demoteArg {
			continue
		}
		req = append(req, r)
	}
	out.InputSchema.Required = req
	return out
}

// wrap builds the tool-handler that performs authz + context setup, then
// delegates to upstream's tool handler. dsType == "" skips the
// datasource resolution and tenant-type checks (org-only path); a
// non-empty dsType triggers datasource resolution: caller-supplied
// argName overrides; otherwise the first live datasource of dsType is
// picked and its UID injected.
func (b *gfBinder) wrap(role authz.Role, tenantType authz.TenantType, dsType grafana.DatasourceType, argName string, upstream mcpgrafana.Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		needsTenant := dsType != ""
		org, errRes := b.requireOrg(ctx, req, role, tenantType, needsTenant)
		if errRes != nil {
			return errRes, nil
		}
		ctx = b.attachGrafana(ctx, org.OrgID)
		if dsType == "" {
			return upstream.Handler(ctx, req)
		}

		opts := grafana.RequestOpts{OrgID: org.OrgID, Caller: authz.CallerSubject(ctx)}

		if uid := req.GetString(argName, ""); uid != "" {
			ds, err := b.grafana.LookupDatasourceByUID(ctx, opts, uid)
			if err != nil {
				return mcp.NewToolResultErrorFromErr(argName, err), nil
			}
			if !grafana.MatchesType(ds, dsType) {
				return mcp.NewToolResultError(fmt.Sprintf("datasource %q is type %q, not compatible with %s tools", uid, ds.Type, dsType)), nil
			}
			return upstream.Handler(ctx, req)
		}

		dss, err := b.grafana.ListDatasources(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("list datasources", err), nil
		}
		matches := grafana.FilterDatasourcesByType(dss, dsType)
		if len(matches) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("org %q has no %s datasource", org.Name, dsType)), nil
		}
		if err := injectArg(&req, argName, matches[0].UID); err != nil {
			return mcp.NewToolResultErrorFromErr("malformed arguments", err), nil
		}
		return upstream.Handler(ctx, req)
	}
}

// requireOrg resolves the "org" arg, runs authz, and (when checkTenant
// is true) gates on tenantType. Returns a populated *CallToolResult on
// failure that the caller should propagate; otherwise (Organization,
// nil).
func (b *gfBinder) requireOrg(ctx context.Context, req mcp.CallToolRequest, role authz.Role, tenantType authz.TenantType, checkTenant bool) (authz.Organization, *mcp.CallToolResult) {
	orgRef, err := req.RequireString("org")
	if err != nil {
		return authz.Organization{}, mcp.NewToolResultErrorFromErr("missing arg", err)
	}
	org, err := b.authorizer.RequireOrg(ctx, orgRef, role)
	if err != nil {
		return authz.Organization{}, mcp.NewToolResultErrorFromErr("authz", err)
	}
	if checkTenant && tenantType != "" && !org.HasTenantType(tenantType) {
		return authz.Organization{}, mcp.NewToolResultError(fmt.Sprintf("org %q has no tenant of type %q — tool unavailable", orgRef, tenantType))
	}
	return org, nil
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
