package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/giantswarm/mcp-oauth/handler"
	brand "github.com/giantswarm/mcp-toolkit/brand/gosdk"
	"github.com/giantswarm/mcp-toolkit/httpx"
	policy "github.com/giantswarm/mcp-toolkit/middleware/gosdk"
	"github.com/giantswarm/mcp-toolkit/toolpolicy"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
	"github.com/giantswarm/mcp-observability-platform/internal/tools"
)

type Config struct {
	Logger           *slog.Logger
	Authorizer       authz.Authorizer
	Grafana          grafana.Client
	GrafanaURL       string
	GrafanaAPIKey    string
	GrafanaBasicAuth *url.Userinfo
	Version          string
	// ToolTimeout is the handler deadline; nonpositive disables it.
	ToolTimeout time.Duration
	// MaxResponseBytes limits the complete JSON result; nonpositive disables it.
	MaxResponseBytes int
	DisabledTools    map[string]bool
}

type policyRegistrar struct {
	server *mcp.Server
	wrap   func(mcp.ToolHandler) mcp.ToolHandler
}

func (r policyRegistrar) AddTool(tool *mcp.Tool, next mcp.ToolHandler) {
	r.server.AddTool(tool, r.wrap(next))
}

// New registers native tools and handlers. The caller owns transport lifecycle.
func New(configuration Config) (*mcp.Server, error) {
	if configuration.Logger == nil {
		return nil, errors.New("server: Logger is required")
	}
	if configuration.Authorizer == nil {
		return nil, errors.New("server: Authorizer is required")
	}
	if configuration.Grafana == nil {
		return nil, errors.New("server: Grafana is required")
	}
	if configuration.Version == "" {
		configuration.Version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-observability-platform", Version: configuration.Version, Icons: []mcp.Icon{brand.GiantSwarmIcon()}}, &mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)})
	timeout := configuration.ToolTimeout
	if timeout == 0 {
		timeout = -1
	}
	wrapPolicy := policy.New(toolpolicy.Options{Timeout: timeout, MaxResultBytes: configuration.MaxResponseBytes, Logger: configuration.Logger, User: validatedIdentity})
	registrar := policyRegistrar{server: server, wrap: func(next mcp.ToolHandler) mcp.ToolHandler {
		return middleware.Instrument()(wrapPolicy(middleware.RequireCaller()(next)))
	}}
	if err := tools.RegisterAll(registrar, configuration.Authorizer, configuration.Grafana, configuration.GrafanaURL, configuration.GrafanaAPIKey, configuration.GrafanaBasicAuth, configuration.DisabledTools); err != nil {
		return nil, fmt.Errorf("server: register tools: %w", err)
	}
	return server, nil
}

func validatedIdentity(ctx context.Context) slog.LogValuer {
	user, ok := handler.UserInfoFromContext(ctx)
	if !ok || user == nil {
		return nil
	}
	return user
}

// StreamableHTTPHandler uses native stateless transport and Origin validation.
// Authentication is enforced by the caller's OAuth middleware.
func StreamableHTTPHandler(server *mcp.Server) http.Handler {
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true})
	return httpx.ProtectOrigin(transport)
}
