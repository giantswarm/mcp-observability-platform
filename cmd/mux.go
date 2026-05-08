package cmd

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-toolkit/httpx"
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

// runHTTP launches srv via httpx.Run in a goroutine. A bind failure or
// unexpected server error triggers abort so the parent shutdownCtx cancels
// and the rest of the lifecycle drains. The returned channel emits the
// final error (nil on graceful shutdown) once the server has stopped.
func runHTTP(ctx context.Context, srv *http.Server, drain time.Duration, label string, logger *slog.Logger, abort context.CancelFunc) <-chan error {
	done := make(chan error, 1)
	go func() {
		logger.Info(label+" listening", "addr", srv.Addr)
		err := httpx.Run(ctx, srv, drain)
		if err != nil {
			logger.Error(label+" server failed", "error", err)
			abort()
		}
		done <- err
	}()
	return done
}
