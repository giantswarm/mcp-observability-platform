package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
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
	Version    string
	// Audit sinks one Record per tool call. Nil is allowed — the audit
	// middleware then degrades to a pass-through.
	Audit *audit.Logger
	// ToolTimeout is the per-tool-handler context deadline. 0 disables
	// per-handler timeouts (ctx passes through unchanged). Callers
	// typically feed middleware.ToolTimeoutFromEnv() here.
	ToolTimeout time.Duration
	// MaxResponseBytes caps each tool response's TextContent size. 0
	// disables capping. Callers typically feed
	// middleware.MaxResponseBytesFromEnv() here.
	MaxResponseBytes int
}

// New builds the core MCP server and registers tools + resource templates +
// prompts. Transport wrapping (streamable-HTTP, SSE, stdio) is the caller's
// concern — use `StreamableHTTPHandler` / `SSEHandler` or drive stdio via
// `mcpsrv.ServeStdio` directly.
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

	deps := &tools.Deps{
		Authorizer: cfg.Authorizer,
		Grafana:    cfg.Grafana,
	}

	// Only `WithToolCapabilities` is advertised — this MCP exposes tools,
	// not resources or prompts. Rationale + explicit scope: see
	// docs/roadmap.md "Out of scope".
	//
	// listChanged is false because the tool set is built once at startup.
	// Flip to true only when a feature actually emits
	// notifications/tools/list_changed.
	//
	// Middleware stack (outermost first):
	//   1. WithRecovery()                   — panic guard (mcp-go).
	//   2. middleware.Instrument(cfg.Audit) — span + metric + audit per call.
	//                                         Classify(res,err) is computed
	//                                         once and fanned out so span
	//                                         status, metric label, and audit
	//                                         outcome stay in sync.
	//   3. middleware.RequireCaller()       — fail-closed authentication.
	//                                         Inside Instrument so denials
	//                                         still emit metrics + audit
	//                                         records (Classify routes them
	//                                         as user_error via IsError).
	//   4. middleware.ResponseCap()         — replace oversized text content
	//                                         with a structured
	//                                         response_too_large payload.
	//   5. middleware.ToolTimeout()         — per-handler context deadline.
	//                                         Innermost so Instrument classifies
	//                                         timeouts as system_error.
	mcp := mcpsrv.NewMCPServer(
		"mcp-observability-platform",
		cfg.Version,
		mcpsrv.WithToolCapabilities(false),
		mcpsrv.WithRecovery(),
		mcpsrv.WithToolHandlerMiddleware(middleware.Instrument(cfg.Audit)),
		mcpsrv.WithToolHandlerMiddleware(middleware.RequireCaller()),
		mcpsrv.WithToolHandlerMiddleware(middleware.ResponseCap(cfg.MaxResponseBytes)),
		mcpsrv.WithToolHandlerMiddleware(middleware.ToolTimeout(cfg.ToolTimeout)),
	)

	tools.RegisterAll(mcp, deps)

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
