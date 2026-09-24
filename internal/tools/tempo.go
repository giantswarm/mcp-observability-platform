// Package tools — tempo.go: register Tempo's own MCP tools (/api/mcp)
// through the same gfBinder.bindDatasourceTool path the upstream
// Grafana tools use. The per-tool handler is just a ProxiedClient cache
// lookup + forward; gfBinder owns authz, org→OrgID, the datasource UID
// lookup, and the per-call ctx attachment.
//
// We don't use mcp-grafana's NewToolManager / InitializeAndRegister*
// path because that path's discovery is single-OrgID-per-session and
// pre-builds a private ProxiedClient cache keyed by UID — and our
// model is multi-org per Grafana, picked per call.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const tempoMCPPath = "/api/mcp"

// tempoClients caches one mcp-grafana ProxiedClient per Tempo
// datasource UID. The transport reads OrgID/auth from per-call ctx
// (attached by gfBinder.wrap), so a single client serves any caller
// whose org points at that UID.
type tempoClients struct {
	grafanaURL string
	mu         sync.Mutex
	cache      map[string]*mcpgrafana.ProxiedClient
}

// tempoDiscoveryRetry is the minimum gap between two deferred discovery
// attempts after a failure, so a Grafana without Tempo MCP does not
// re-dial on every tools/list.
const tempoDiscoveryRetry = 30 * time.Second

// registerTempoTools dials a seed Tempo to enumerate its MCP tool list,
// then registers each tool through gfBinder.bindDatasourceTool — same
// authz, org-arg, tenant-type and UID resolution as every other
// delegated datasource tool. Skipped silently on any startup-discovery
// failure (no orgs, no Tempo datasource, chart not yet rolled out →
// 404 on /api/mcp) so the rest of the surface still boots.
//
// In JWT auth mode there is no Grafana credential at startup, so
// discovery is deferred to the first tools/list or tools/call carrying a
// caller token (see tempoDiscovery). The tools are added before that
// request is served; the server's listChanged capability notifies other
// sessions.
func registerTempoTools(ctx context.Context, s *server.MCPServer, logger *slog.Logger, b *gfBinder, ol authz.OrgLister) error {
	c := &tempoClients{
		grafanaURL: b.url,
		cache:      make(map[string]*mcpgrafana.ProxiedClient),
	}
	if b.auth.JWTHeader == "" {
		discoverTempoTools(ctx, s, logger, b, ol, c)
		return nil
	}
	hooks := s.GetHooks()
	if hooks == nil {
		logger.Warn("tempo MCP not registered", "reason", "deferred discovery needs server hooks")
		return nil
	}
	d := &tempoDiscovery{
		discover: func(ctx context.Context) bool { return discoverTempoTools(ctx, s, logger, b, ol, c) },
		now:      time.Now,
	}
	hooks.AddBeforeListTools(func(ctx context.Context, _ any, _ *mcp.ListToolsRequest) { d.run(ctx) })
	hooks.AddBeforeCallTool(func(ctx context.Context, _ any, _ *mcp.CallToolRequest) { d.run(ctx) })
	logger.Info("Tempo MCP discovery deferred to the first caller (Grafana auth mode: jwt)")
	return nil
}

// discoverTempoTools finds a seed Tempo reachable with ctx's credentials,
// dials it, and registers its tools. Returns false (after logging) on any
// failure.
func discoverTempoTools(ctx context.Context, s *server.MCPServer, logger *slog.Logger, b *gfBinder, ol authz.OrgLister, c *tempoClients) bool {
	opts := grafana.RequestOpts{Caller: authz.CallerSubject(ctx)}
	seedOrgID, seedUID, err := findSeedTempoUID(ctx, b.grafana, ol, opts)
	if err != nil {
		logger.Warn("tempo MCP not registered", "reason", err)
		return false
	}
	seed, err := c.clientFor(b.attachGrafana(ctx, seedOrgID), seedUID)
	if err != nil {
		logger.Warn("tempo MCP not registered", "uid", seedUID, "error", err)
		return false
	}

	tools := seed.ListTools()
	for _, t := range tools {
		t.Annotations = readOnlyToolAnnotation
		b.bindDatasourceTool(s, authz.RoleViewer, authz.TenantTypeData, grafana.DSTypeTempo, datasourceUIDArg, mcpgrafana.Tool{
			Tool:    t,
			Handler: c.handler(t.Name),
		})
	}
	logger.Info("registered Tempo MCP tools", "count", len(tools))
	return true
}

// tempoDiscovery runs deferred Tempo discovery (JWT auth mode) until it
// succeeds once. Requests without a caller token are skipped; a failure
// is retried no sooner than tempoDiscoveryRetry later. Concurrent callers
// wait on mu so the first successful run's tools are in their response.
type tempoDiscovery struct {
	discover func(ctx context.Context) bool
	now      func() time.Time

	mu    sync.Mutex
	done  bool
	retry time.Time
}

func (d *tempoDiscovery) run(ctx context.Context) {
	if grafana.UserTokenFromContext(ctx) == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done || d.now().Before(d.retry) {
		return
	}
	if d.discover(ctx) {
		d.done = true
		return
	}
	d.retry = d.now().Add(tempoDiscoveryRetry)
}

// handler reads the datasourceUid that gfBinder.wrap injected, finds
// (or dials) the matching ProxiedClient, and forwards the call. name
// is the upstream tool name as exposed by Tempo's MCP server.
func (c *tempoClients) handler(name string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		uid, _ := args[datasourceUIDArg].(string)
		if uid == "" {
			return mcp.NewToolResultError("internal: datasourceUid missing — gfBinder did not inject it"), nil
		}
		client, err := c.clientFor(ctx, uid)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("tempo MCP", err), nil
		}
		forward := make(map[string]any, len(args))
		maps.Copy(forward, args)
		delete(forward, datasourceUIDArg)
		return client.CallTool(ctx, name, forward)
	}
}

func (c *tempoClients) clientFor(ctx context.Context, uid string) (*mcpgrafana.ProxiedClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.cache[uid]; ok {
		return cl, nil
	}
	cl, err := c.dial(ctx, uid)
	if err != nil {
		return nil, err
	}
	c.cache[uid] = cl
	return cl, nil
}

// dial connects to Tempo's MCP server through Grafana's datasource
// proxy. ctx must already carry a GrafanaConfig (gfBinder.attachGrafana
// or our seed-dial ctx) — the transport reads it for OrgID/auth.
func (c *tempoClients) dial(ctx context.Context, uid string) (*mcpgrafana.ProxiedClient, error) {
	mcpURL := strings.TrimRight(c.grafanaURL, "/") + "/api/datasources/proxy/uid/" + uid + tempoMCPPath
	return mcpgrafana.NewProxiedClient(ctx, uid, "tempo-"+uid, string(grafana.DSTypeTempo), mcpURL)
}

// findSeedTempoUID returns any (orgID, tempo UID) pair from the live
// datasource list — used at startup to enumerate Tempo's tool list.
// Per-call routing uses the caller's own org via gfBinder. opts.Caller
// is set in JWT auth mode, where the lookup runs as the first caller and
// orgs they cannot read are skipped.
func findSeedTempoUID(ctx context.Context, gc grafana.Client, ol authz.OrgLister, opts grafana.RequestOpts) (int64, string, error) {
	orgs, err := ol.List(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("list orgs: %w", err)
	}
	for _, org := range orgs {
		if !org.HasTenantType(authz.TenantTypeData) {
			continue
		}
		opts.OrgID = org.OrgID
		dss, err := gc.ListDatasources(ctx, opts)
		if err != nil {
			continue
		}
		matches := grafana.FilterDatasourcesByType(dss, grafana.DSTypeTempo)
		if len(matches) == 0 {
			continue
		}
		return org.OrgID, matches[0].UID, nil
	}
	return 0, "", errors.New("no org has a Tempo datasource")
}
