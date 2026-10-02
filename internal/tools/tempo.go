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

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const tempoMCPPath = "/api/mcp"

// tempoClients caches one mcp-grafana ProxiedClient per (org, Tempo
// datasource UID). The client pins the org it was dialled for (the
// proxy sends that X-Grafana-Org-Id regardless of the per-call ctx), and
// a datasource UID is only unique within an org, so both form the key.
// Auth still comes from the per-call ctx attached by gfBinder.wrap.
type tempoClients struct {
	grafanaURL string
	mu         sync.Mutex
	cache      map[tempoClientKey]*mcpgrafana.ProxiedClient
}

type tempoClientKey struct {
	orgID int64
	uid   string
}

// registerTempoTools dials a seed Tempo to enumerate its MCP tool list,
// then registers each tool through gfBinder.bindDatasourceTool — same
// authz, org-arg, tenant-type and UID resolution as every other
// delegated datasource tool. Skipped silently on any startup-discovery
// failure (no orgs, no Tempo datasource, chart not yet rolled out →
// 404 on /api/mcp) so the rest of the surface still boots.
func registerTempoTools(ctx context.Context, s *server.MCPServer, logger *slog.Logger, b *gfBinder, ol authz.OrgLister) error {
	seedOrgID, seedUID, err := findSeedTempoUID(ctx, b.grafana, ol)
	if err != nil {
		logger.Warn("tempo MCP not registered", "reason", err)
		return nil
	}
	c := &tempoClients{
		grafanaURL: b.url,
		cache:      make(map[tempoClientKey]*mcpgrafana.ProxiedClient),
	}
	seed, err := c.clientFor(b.attachGrafana(ctx, seedOrgID), seedUID)
	if err != nil {
		logger.Warn("tempo MCP not registered", "uid", seedUID, "error", err)
		return nil
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
	return nil
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
	key := tempoClientKey{orgID: mcpgrafana.GrafanaConfigFromContext(ctx).OrgID, uid: uid}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.cache[key]; ok {
		return cl, nil
	}
	cl, err := c.dial(ctx, key)
	if err != nil {
		return nil, err
	}
	c.cache[key] = cl
	return cl, nil
}

// dial connects to Tempo's MCP server through Grafana's datasource
// proxy, pinned to key.orgID. ctx must already carry a GrafanaConfig
// (gfBinder.attachGrafana or our seed-dial ctx) — the transport reads
// it for auth.
func (c *tempoClients) dial(ctx context.Context, key tempoClientKey) (*mcpgrafana.ProxiedClient, error) {
	mcpURL := strings.TrimRight(c.grafanaURL, "/") + "/api/datasources/proxy/uid/" + key.uid + tempoMCPPath
	return mcpgrafana.NewProxiedClient(ctx, key.orgID, key.uid, "tempo-"+key.uid, string(grafana.DSTypeTempo), mcpURL)
}

// findSeedTempoUID returns any (orgID, tempo UID) pair from the live
// datasource list — used at startup to enumerate Tempo's tool list.
// Per-call routing uses the caller's own org via gfBinder.
func findSeedTempoUID(ctx context.Context, gc grafana.Client, ol authz.OrgLister) (int64, string, error) {
	orgs, err := ol.List(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("list orgs: %w", err)
	}
	for _, org := range orgs {
		if !org.HasTenantType(authz.TenantTypeData) {
			continue
		}
		dss, err := gc.ListDatasources(ctx, grafana.RequestOpts{OrgID: org.OrgID})
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
