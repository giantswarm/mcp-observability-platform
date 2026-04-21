// Package server wires the MCP protocol layer: it registers tools and resource
// templates against a mark3labs/mcp-go server and returns an http.Handler that
// can be mounted behind mcp-oauth's ValidateToken middleware.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	oauth "github.com/giantswarm/mcp-oauth"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// Config configures a new MCP HTTP server.
type Config struct {
	Logger   *slog.Logger
	Resolver *authz.Resolver
	Grafana  *grafana.Client
	Version  string
}

// New builds the MCP server, registers tools + resource templates, and returns
// an http.Handler serving the streamable-http transport on the /mcp path.
// Callers are expected to wrap the returned handler with OAuth middleware.
func New(cfg Config) (http.Handler, error) {
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

	deps := &deps{
		log:      cfg.Logger,
		resolver: cfg.Resolver,
		grafana:  cfg.Grafana,
	}

	// Capabilities advertise static surfaces only. listChanged / subscribe are
	// false because this MCP never emits notifications/{tools,resources,prompts}/
	// list_changed or notifications/resources/updated — the tool / resource /
	// prompt set is built once at startup. Flip to true when a feature PR wires
	// real change notifications.
	mcp := mcpsrv.NewMCPServer(
		"mcp-observability-platform",
		cfg.Version,
		mcpsrv.WithResourceCapabilities(false, false),
		mcpsrv.WithToolCapabilities(false),
		mcpsrv.WithPromptCapabilities(false),
	)

	registerTools(mcp, deps)
	registerResources(mcp, deps)
	registerPrompts(mcp, deps)

	return mcpsrv.NewStreamableHTTPServer(
		mcp,
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithHTTPContextFunc(promoteOAuthCaller),
	), nil
}

// deps bundles the handler-scoped dependencies so tool/resource registration
// stays concise.
type deps struct {
	log      *slog.Logger
	resolver *authz.Resolver
	grafana  *grafana.Client
}

// promoteOAuthCaller lifts the UserInfo attached by mcp-oauth's ValidateToken
// middleware onto the context that mcp-go passes to tool/resource handlers.
// Callers that reach a tool without a valid identity will be rejected at the
// handler boundary (callerGroups returns nil, and the resolver returns an
// authorisation error).
func promoteOAuthCaller(ctx context.Context, r *http.Request) context.Context {
	if ui, ok := oauth.UserInfoFromContext(r.Context()); ok {
		return withCaller(ctx, ui)
	}
	return ctx
}

// callerGroups returns the caller's groups or nil if identity is missing.
// Nil is treated as "no access" everywhere downstream.
func callerGroups(ctx context.Context) []string {
	ui, ok := CallerFromContext(ctx)
	if !ok || ui == nil {
		return nil
	}
	return ui.Groups
}
