// Package server wires the MCP protocol layer: it registers tools and resource
// templates against a mark3labs/mcp-go server. Transport wrapping (streamable-
// HTTP, SSE, stdio) is the caller's concern — this package returns the core
// `*mcpsrv.MCPServer` plus convenience handlers for the HTTP transports.
package server

import (
	"errors"
	"log/slog"
	"net/http"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/identity"
	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
	"github.com/giantswarm/mcp-observability-platform/internal/tools"
)

// Config configures a new MCP HTTP server.
type Config struct {
	Logger   *slog.Logger
	Resolver *authz.Resolver
	Grafana  *grafana.Client
	Version  string
	// Audit sinks one Record per tool call. Nil is allowed — the audit
	// middleware then degrades to a pass-through.
	Audit *audit.Logger
}

// New builds the core MCP server and registers tools + resource templates +
// prompts. Transport wrapping (streamable-HTTP, SSE, stdio) is the caller's
// concern — use `StreamableHTTPHandler` / `SSEHandler` or drive stdio via
// `mcpsrv.ServeStdio` directly.
func New(cfg Config) (*mcpsrv.MCPServer, error) {
	if cfg.Logger == nil {
		return nil, errors.New("server: Logger is required")
	}
	if cfg.Resolver == nil {
		return nil, errors.New("server: Resolver is required")
	}
	if cfg.Grafana == nil {
		return nil, errors.New("server: Grafana is required")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}

	deps := &tools.Deps{
		Resolver: cfg.Resolver,
		Grafana:  cfg.Grafana,
	}

	// Capabilities advertise static surfaces only. listChanged / subscribe are
	// false because this MCP never emits notifications/{tools,resources,prompts}/
	// list_changed or notifications/resources/updated — the tool / resource /
	// prompt set is built once at startup. Flip to true when a feature PR wires
	// real change notifications.
	//
	// Middleware stack (outermost first):
	//   1. WithRecovery()         — mcp-go's built-in panic guard.
	//   2. middleware.Tracing()   — OTEL span per tool call.
	//   3. middleware.Metrics()   — Prometheus counter + histogram per tool call.
	//   4. middleware.Audit()     — structured JSON record per tool call (if cfg.Audit).
	// Ordered so a panic is caught before the span/metric/audit close, the
	// span wraps the metric timing, and the audit middleware's Duration
	// reflects handler time only — matching the Metrics histogram label.
	mcp := mcpsrv.NewMCPServer(
		"mcp-observability-platform",
		cfg.Version,
		mcpsrv.WithResourceCapabilities(false, false),
		mcpsrv.WithToolCapabilities(false),
		mcpsrv.WithPromptCapabilities(false),
		mcpsrv.WithRecovery(),
		mcpsrv.WithToolHandlerMiddleware(middleware.Tracing()),
		mcpsrv.WithToolHandlerMiddleware(middleware.Metrics()),
		mcpsrv.WithToolHandlerMiddleware(middleware.Audit(cfg.Audit)),
	)

	tools.RegisterAll(mcp, deps)
	registerResources(mcp)
	registerPrompts(mcp)

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
		mcpsrv.WithHTTPContextFunc(identity.PromoteOAuthCaller),
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
		mcpsrv.WithSSEContextFunc(identity.PromoteOAuthCaller),
	)
}
