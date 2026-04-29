package server

import (
	"context"
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

type Config struct {
	Logger     *slog.Logger
	Authorizer authz.Authorizer
	// OrgLister is used at startup only — the Tempo binder needs one
	// org with a Tempo datasource to enumerate Tempo's MCP tool list
	// against. Per-call routing uses the caller's own org via gfBinder.
	OrgLister authz.OrgLister
	Grafana   grafana.Client
	// GrafanaURL / GrafanaAPIKey / GrafanaBasicAuth are forwarded to the
	// gfBinder, which builds an upstream mcpgrafana client per call.
	// APIKey and BasicAuth are mutually exclusive; exactly one must be set.
	GrafanaURL       string
	GrafanaAPIKey    string
	GrafanaBasicAuth *url.Userinfo
	Version          string
	// ToolTimeout: 0 disables the per-handler deadline.
	ToolTimeout time.Duration
	// MaxResponseBytes: 0 disables response capping.
	MaxResponseBytes int
}

// New constructs the tools-only MCP server. Transport wrapping is the
// caller's concern — use StreamableHTTPHandler / SSEHandler, or drive
// stdio via mcpsrv.ServeStdio. ctx is used for one-shot startup work
// (Tempo MCP discovery) and is not retained.
func New(ctx context.Context, cfg Config) (*mcpsrv.MCPServer, error) {
	if cfg.Logger == nil {
		return nil, errors.New("server: Logger is required")
	}
	if cfg.Authorizer == nil {
		return nil, errors.New("server: Authorizer is required")
	}
	if cfg.OrgLister == nil {
		return nil, errors.New("server: OrgLister is required")
	}
	if cfg.Grafana == nil {
		return nil, errors.New("server: Grafana is required")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}

	// Middleware order (outermost first): Recovery → Instrument →
	// RequireCaller → ResponseCap → ToolTimeout. RequireCaller sits
	// inside Instrument so denials still emit a metric + audit line;
	// ToolTimeout sits innermost so its deadline-exceeded errors flow
	// up through Instrument as a Go error (is_error=true).
	mcp := mcpsrv.NewMCPServer(
		"mcp-observability-platform",
		cfg.Version,
		// WithToolCapabilities advertises the "tools" capability in the
		// initialize handshake; required because we register tools but
		// no resources/prompts.
		mcpsrv.WithToolCapabilities(true),
		mcpsrv.WithRecovery(),
		mcpsrv.WithToolHandlerMiddleware(middleware.Instrument(cfg.Logger)),
		mcpsrv.WithToolHandlerMiddleware(middleware.RequireCaller()),
		mcpsrv.WithToolHandlerMiddleware(middleware.ResponseCap(cfg.MaxResponseBytes)),
		mcpsrv.WithToolHandlerMiddleware(middleware.ToolTimeout(cfg.ToolTimeout)),
	)

	if err := tools.RegisterAll(ctx, mcp, cfg.Logger, cfg.Authorizer, cfg.OrgLister, cfg.Grafana, cfg.GrafanaURL, cfg.GrafanaAPIKey, cfg.GrafanaBasicAuth); err != nil {
		return nil, fmt.Errorf("server: register tools: %w", err)
	}

	return mcp, nil
}

// StreamableHTTPHandler mounts the streamable-HTTP transport at /mcp.
// The handler trusts the caller identity already on the request context,
// so it MUST be gated behind mcp-oauth's ValidateToken.
func StreamableHTTPHandler(mcp *mcpsrv.MCPServer) http.Handler {
	return mcpsrv.NewStreamableHTTPServer(
		mcp,
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithHTTPContextFunc(middleware.InjectCallerFromRequest),
	)
}

// SSEHandler mounts the SSE transport. SSE needs both /sse (event
// stream) and /message (client→server posts) on the same handler;
// mcp-go routes between them by path. Caller gates with OAuth.
func SSEHandler(mcp *mcpsrv.MCPServer) http.Handler {
	return mcpsrv.NewSSEServer(
		mcp,
		mcpsrv.WithSSEEndpoint("/sse"),
		mcpsrv.WithMessageEndpoint("/message"),
		mcpsrv.WithSSEContextFunc(middleware.InjectCallerFromRequest),
	)
}
