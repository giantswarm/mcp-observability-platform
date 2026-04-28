package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
	"github.com/giantswarm/mcp-observability-platform/internal/tools"
)

// Config configures a new MCP HTTP server.
type Config struct {
	Logger     *slog.Logger
	Authorizer authz.Authorizer
	Grafana    grafana.Client
	// GrafanaURL is the base URL the delegated tool handlers use to build
	// per-request upstream GrafanaClients. Same value as the Grafana
	// client's URL; threaded through here because the binder constructs
	// upstream's client per-call.
	GrafanaURL string
	// GrafanaAPIKey and GrafanaBasicAuth are mutually exclusive; exactly
	// one must be set. Threaded through for the binder's per-request
	// upstream GrafanaClient construction.
	GrafanaAPIKey    string
	GrafanaBasicAuth *url.Userinfo
	Version          string
	// ToolTimeout is the per-tool-handler context deadline. 0 disables
	// per-handler timeouts (ctx passes through unchanged).
	ToolTimeout time.Duration
	// MaxResponseBytes caps each tool response's TextContent size. 0
	// disables capping.
	MaxResponseBytes int
}

// New builds the core MCP server and registers the tool surface. This
// MCP exposes only tools. Transport wrapping (streamable-HTTP, SSE, stdio)
// is the caller's concern — use `StreamableHTTPHandler` / `SSEHandler`
// or drive stdio via `mcpsrv.ServeStdio` directly.
func New(cfg Config) (*mcpsrv.MCPServer, error) {
	if cfg.Logger == nil {
		return nil, errors.New("server: Logger is required")
	}
	if cfg.Authorizer == nil {
		return nil, errors.New("server: Authorizer is required")
	}
	if cfg.Grafana == nil {
		return nil, errors.New("server: Grafana is required")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}

	// Middleware stack, outer→inner. Order matters:
	//   1. WithRecovery       — panic guard (mcp-go).
	//   2. Instrument         — span + metrics + tool_call audit line. Outer
	//                           so RequireCaller denials are still observed.
	//   3. RequireCaller      — fail closed if no authenticated caller.
	//   4. ResponseCap        — replace oversized text with response_too_large.
	//   5. ToolTimeout        — per-handler context deadline (innermost).
	mcp := mcpsrv.NewMCPServer(
		"mcp-observability-platform",
		cfg.Version,
		mcpsrv.WithToolCapabilities(true),
		mcpsrv.WithRecovery(),
		mcpsrv.WithToolHandlerMiddleware(middleware.Instrument(cfg.Logger)),
		mcpsrv.WithToolHandlerMiddleware(middleware.RequireCaller()),
		mcpsrv.WithToolHandlerMiddleware(middleware.ResponseCap(cfg.MaxResponseBytes)),
		mcpsrv.WithToolHandlerMiddleware(middleware.ToolTimeout(cfg.ToolTimeout)),
	)

	if err := tools.RegisterAll(mcp, cfg.Authorizer, cfg.Grafana, cfg.GrafanaURL, cfg.GrafanaAPIKey, cfg.GrafanaBasicAuth); err != nil {
		return nil, fmt.Errorf("server: register tools: %w", err)
	}

	return mcp, nil
}

// StreamableHTTPHandler wraps an MCP server in mcp-go's streamable-HTTP
// transport, mounted at `/mcp`. Caller is expected to gate the returned
// handler behind mcp-oauth's ValidateToken middleware — the handler itself
// trusts whatever identity the HTTP context carries.
func StreamableHTTPHandler(mcp *mcpsrv.MCPServer) http.Handler {
	return mcpsrv.NewStreamableHTTPServer(
		mcp,
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithHTTPContextFunc(authz.PromoteOAuthCaller),
	)
}

// SSEHandler wraps an MCP server in mcp-go's SSE transport. The SSE
// protocol requires two endpoints (`/sse` for the event stream, `/message`
// for client→server posts); both are served by the returned handler and
// routed internally by mcp-go based on path. Caller gates with OAuth.
func SSEHandler(mcp *mcpsrv.MCPServer) http.Handler {
	return mcpsrv.NewSSEServer(
		mcp,
		mcpsrv.WithSSEEndpoint("/sse"),
		mcpsrv.WithMessageEndpoint("/message"),
		mcpsrv.WithSSEContextFunc(authz.PromoteOAuthCaller),
	)
}
