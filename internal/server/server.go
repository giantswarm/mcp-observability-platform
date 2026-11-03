package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/giantswarm/mcp-toolkit/middleware/responsecap"
	"github.com/giantswarm/mcp-toolkit/middleware/timeout"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
	"github.com/giantswarm/mcp-observability-platform/internal/tools"
)

type Config struct {
	Logger     *slog.Logger
	Authorizer authz.Authorizer
	Grafana    grafana.Client
	// GrafanaURL / GrafanaAPIKey / GrafanaBasicAuth / GrafanaJWTHeader
	// are forwarded to the gfBinder, which builds an upstream mcpgrafana
	// client per call. APIKey, BasicAuth and JWTHeader are mutually
	// exclusive; exactly one must be set. JWTHeader selects JWT auth
	// mode: the caller's own token is forwarded in that header.
	GrafanaURL       string
	GrafanaAPIKey    string
	GrafanaBasicAuth *url.Userinfo
	GrafanaJWTHeader string
	Version          string
	// ToolTimeout: 0 disables the per-handler deadline.
	ToolTimeout time.Duration
	// MaxResponseBytes: 0 disables response capping.
	MaxResponseBytes int
	// DisabledTools is a name lookup of tools to skip at registration.
	// Sourced from the --disabled-tools flag (cmd/serve.go); nil = no
	// filter.
	DisabledTools map[string]bool
}

// New constructs the tools-only MCP server. Transport wrapping is the
// caller's concern — use StreamableHTTPHandler / SSEHandler, or drive
// stdio via mcpsrv.ServeStdio.
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
		// Empty Hooks so RegisterAll can attach its own via GetHooks
		// (deferred Tempo discovery in JWT auth mode).
		mcpsrv.WithHooks(&mcpsrv.Hooks{}),
		mcpsrv.WithToolHandlerMiddleware(middleware.Instrument(cfg.Logger)),
		mcpsrv.WithToolHandlerMiddleware(middleware.RequireCaller()),
		mcpsrv.WithToolHandlerMiddleware(responsecap.New(responsecap.Options{
			Limit: cfg.MaxResponseBytes,
			Hint: func(string) string {
				return "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range"
			},
		})),
		mcpsrv.WithToolHandlerMiddleware(timeout.New(cfg.ToolTimeout)),
	)

	if err := tools.RegisterAll(mcp, cfg.Authorizer, cfg.Grafana, cfg.GrafanaURL, tools.GrafanaAuth{
		APIKey:    cfg.GrafanaAPIKey,
		BasicAuth: cfg.GrafanaBasicAuth,
		JWTHeader: cfg.GrafanaJWTHeader,
	}, cfg.DisabledTools); err != nil {
		return nil, fmt.Errorf("server: register tools: %w", err)
	}

	return mcp, nil
}

// StreamableHTTPHandler mounts the streamable-HTTP transport at /mcp.
// The handler trusts the caller identity already on the request context,
// so it MUST be gated behind mcp-oauth's ValidateToken. resolve is set
// only in JWT auth mode (see middleware.InjectCaller); nil otherwise.
func StreamableHTTPHandler(mcp *mcpsrv.MCPServer, resolve middleware.TokenResolver) http.Handler {
	return mcpsrv.NewStreamableHTTPServer(
		mcp,
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithHTTPContextFunc(middleware.InjectCaller(resolve)),
	)
}

// SSEHandler mounts the SSE transport. SSE needs both /sse (event
// stream) and /message (client→server posts) on the same handler;
// mcp-go routes between them by path. Caller gates with OAuth. resolve
// as for StreamableHTTPHandler.
func SSEHandler(mcp *mcpsrv.MCPServer, resolve middleware.TokenResolver) http.Handler {
	return mcpsrv.NewSSEServer(
		mcp,
		mcpsrv.WithSSEEndpoint("/sse"),
		mcpsrv.WithMessageEndpoint("/message"),
		mcpsrv.WithSSEContextFunc(middleware.InjectCaller(resolve)),
	)
}
