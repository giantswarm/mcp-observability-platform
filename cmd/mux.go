package cmd

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	oauth "github.com/giantswarm/mcp-oauth"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
)

// buildMux returns the single HTTP handler that serves everything: OAuth
// flow + discovery routes, the MCP transport (`/mcp` or `/sse`),
// /metrics, and /healthz + /readyz. Wrapped in otelhttp so inbound W3C
// traceparents become server spans.
//
// One mux instead of two-server-per-concern: at this scale the
// "operational port separate from app port" split was overhead, not
// safety. Kubernetes probes + Prometheus scraping work fine on the same
// listener as the MCP traffic.
func buildMux(transport string, mcp *mcpsrv.MCPServer, oauthHandler *oauth.Handler, dexIssuerURL string, gf grafana.Client, listOrgs func(context.Context) (int, error), cacheAlive *atomic.Bool) http.Handler {
	mux := http.NewServeMux()

	resourcePath := "/mcp"
	if transport == transportSSE {
		resourcePath = "/sse"
	}
	// OAuth flow + RFC 9728/8414 discovery — single bundle helper from
	// mcp-oauth (v0.2.106). Replaces five HandleFunc lines + two
	// Register*Routes calls.
	oauthHandler.RegisterOAuthRoutes(mux, oauth.OAuthRoutesOptions{
		MCPPath:         resourcePath,
		IncludeMetadata: true,
	})

	// MCP transport (OAuth-gated).
	switch transport {
	case transportStreamableHTTP:
		mux.Handle("/mcp", oauthHandler.ValidateToken(server.StreamableHTTPHandler(mcp)))
	case transportSSE:
		sseHandler := server.SSEHandler(mcp)
		mux.Handle("/sse", oauthHandler.ValidateToken(sseHandler))
		mux.Handle("/message", oauthHandler.ValidateToken(sseHandler))
	}

	// Health + metrics. Unauthenticated by design: the cluster network
	// policy is the trust boundary.
	mountHealth(mux, dexIssuerURL, gf, listOrgs, cacheAlive)
	mux.Handle("/metrics", observability.MetricsHandler())

	return otelhttp.NewHandler(mux, "mcp",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

// runListenAndServe runs srv.ListenAndServe in a goroutine and cancels
// shutdownCancel on a non-clean exit so a failed bind takes the whole
// process down rather than leaving the server in a half-up state.
func runListenAndServe(srv *http.Server, logger *slog.Logger, shutdownCancel context.CancelFunc) {
	go func() {
		logger.Info("HTTP listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			shutdownCancel()
		}
	}()
}
