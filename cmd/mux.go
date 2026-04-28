package cmd

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
)

// buildMCPMux wraps the OAuth + MCP routes in otelhttp so inbound W3C
// traceparents become server spans.
func buildMCPMux(transport string, mcp *mcpsrv.MCPServer, oauthHandler *oauth.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/authorize", oauthHandler.ServeAuthorization)
	mux.HandleFunc("/oauth/callback", oauthHandler.ServeCallback)
	mux.HandleFunc("/oauth/token", oauthHandler.ServeToken)
	mux.HandleFunc("/oauth/revoke", oauthHandler.ServeTokenRevocation)
	mux.HandleFunc("/oauth/register", oauthHandler.ServeClientRegistration)

	resourcePath := "/mcp"
	if transport == transportSSE {
		resourcePath = "/sse"
	}
	oauthHandler.RegisterProtectedResourceMetadataRoutes(mux, resourcePath)
	oauthHandler.RegisterAuthorizationServerMetadataRoutes(mux)

	switch transport {
	case transportStreamableHTTP:
		mux.Handle("/mcp", oauthHandler.ValidateToken(server.StreamableHTTPHandler(mcp)))
	case transportSSE:
		sseHandler := server.SSEHandler(mcp)
		mux.Handle("/sse", oauthHandler.ValidateToken(sseHandler))
		mux.Handle("/message", oauthHandler.ValidateToken(sseHandler))
	}

	return otelhttp.NewHandler(mux, "mcp",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

func buildObsMux(lister authz.OrgLister, cacheAlive *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", healthz.CheckHandler{Checker: healthz.Ping})
	mux.Handle("/readyz", healthz.CheckHandler{Checker: readyzChecker(lister, cacheAlive)})
	mux.Handle("/metrics", observability.MetricsHandler())
	return mux
}

// runListenAndServe cancels shutdownCancel on a non-clean exit so a single
// failed bind takes the whole process down rather than leaving one server
// running silently.
func runListenAndServe(srv *http.Server, label string, logger *slog.Logger, shutdownCancel context.CancelFunc) {
	go func() {
		logger.Info(label+" listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(label+" server failed", "error", err)
			shutdownCancel()
		}
	}()
}

// runTwoPhaseShutdown drains MCP first (10s) then the obs server (5s).
// The ordering is load-bearing: by keeping the obs listener up while MCP
// drains, kubelet's liveness probe still hits a live /healthz and a slow
// tool call doesn't trip SIGKILL mid-drain. Merging the servers would
// destroy this property — see review.
func runTwoPhaseShutdown(logger *slog.Logger, mcpServer, obsServer *http.Server) {
	mcpDrainCtx, mcpDrainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer mcpDrainCancel()
	if err := mcpServer.Shutdown(mcpDrainCtx); err != nil {
		logger.Warn("mcp server drain returned error", "error", err)
	}

	obsDrainCtx, obsDrainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer obsDrainCancel()
	if err := obsServer.Shutdown(obsDrainCtx); err != nil {
		logger.Warn("observability server drain returned error", "error", err)
	}
}
