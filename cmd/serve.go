package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server",
	RunE:  runServe,
}

// Transport constants for MCP_TRANSPORT / --transport. mcp-go does not
// export these, so we define our own — matching mcp-kubernetes.
const (
	transportStdio          = "stdio"
	transportSSE            = "sse"
	transportStreamableHTTP = "streamable-http"
)

var (
	flagTransport string
	flagAddr      string
	flagDebug     bool
)

func init() {
	serveCmd.Flags().StringVar(&flagTransport, "transport", envOr("MCP_TRANSPORT", transportStreamableHTTP), transportStdio+" | "+transportSSE+" | "+transportStreamableHTTP)
	serveCmd.Flags().StringVar(&flagAddr, "addr", envOr("MCP_ADDR", ":8080"), "HTTP listen address (serves /mcp + /metrics + /healthz + /readyz + /oauth/*)")
	// DEBUG env is read inside runServe (via loadConfig) so a malformed value
	// fails startup with a clear error rather than silently defaulting.
	serveCmd.Flags().BoolVar(&flagDebug, "debug", false, "enable debug logging (overrides DEBUG env)")
}

// validateTransport rejects unknown MCP_TRANSPORT values.
func validateTransport(transport string) error {
	switch transport {
	case transportStdio, transportSSE, transportStreamableHTTP:
		return nil
	default:
		return fmt.Errorf("transport %q is not supported (want one of: %s, %s, %s)", transport, transportStdio, transportSSE, transportStreamableHTTP)
	}
}

// runServe is the MCP server orchestration entry point. Phase-by-phase
// helpers live in oauth.go / orglister.go / mux.go; runServe reads as a
// single ordered build-then-serve flow.
func runServe(_ *cobra.Command, _ []string) error {
	if err := validateTransport(flagTransport); err != nil {
		return err
	}

	shutdownCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := guardStdioInCluster(flagTransport); err != nil {
		return err
	}
	// --debug on the CLI forces debug on; otherwise DEBUG env (via cfg) wins.
	logger := newLogger(cfg.Debug || flagDebug, cfg.LogFormat)

	ctrlCache, cacheAlive, err := buildOrgCache(shutdownCtx, logger)
	if err != nil {
		return err
	}

	grafanaClient, err := grafana.New(grafana.Config{
		URL:       cfg.GrafanaURL,
		Token:     cfg.GrafanaSAToken,
		BasicAuth: cfg.GrafanaBasicAuth,
	})
	if err != nil {
		return fmt.Errorf("grafana client: %w", err)
	}
	if err := grafanaClient.VerifyServerAdmin(shutdownCtx); err != nil {
		return fmt.Errorf(
			"grafana credential is not server-admin (cannot list all orgs); "+
				"use a server-admin SA token, or set GRAFANA_BASIC_AUTH=admin:password: %w", err)
	}
	logger.Info("Grafana server-admin credential verified")

	// authz uses Grafana as the source of truth for org membership.
	// 30s TTL — role/membership changes propagate within that window.
	authorizer, err := authz.NewAuthorizer(k8sOrgLister{reader: ctrlCache}, grafanaClient, logger, authz.DefaultCacheTTL)
	if err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	oauthHandler, storeClose, err := buildOAuthHandler(shutdownCtx, logger)
	if err != nil {
		return err
	}
	defer func() { _ = storeClose() }()

	// Best-effort OTEL tracing. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is
	// unset. The cluster log pipeline ships stderr to Loki; we don't run
	// a separate OTLP-logs path.
	shutdownOTEL, err := observability.InitTracing(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownOTEL(ctx)
		}()
	}

	basicAuth, err := parseGrafanaBasicAuth(cfg.GrafanaBasicAuth)
	if err != nil {
		return err
	}
	apiKey := cfg.GrafanaSAToken
	if basicAuth != nil {
		// gfBinder requires exactly one of APIKey / BasicAuth, so when
		// BasicAuth is set blank the token here. Same invariant the
		// loader enforces, expressed at construction.
		apiKey = ""
	}

	mcp, err := server.New(server.Config{
		Logger:           logger,
		Authorizer:       authorizer,
		Grafana:          grafanaClient,
		GrafanaURL:       cfg.GrafanaURL,
		GrafanaAPIKey:    apiKey,
		GrafanaBasicAuth: basicAuth,
		Version:          version,
		ToolTimeout:      cfg.ToolTimeout,
		MaxResponseBytes: cfg.MaxResponseBytes,
	})
	if err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	// Stdio: no HTTP surface, no OAuth. Production deploys use streamable-http;
	// stdio is a developer-loop convenience.
	if flagTransport == transportStdio {
		logger.Info("MCP serving on stdio", "transport", transportStdio)
		logger.Warn("stdio transport bypasses OAuth — tool calls will hit authz errors unless the session provides a caller identity")
		return mcpsrv.ServeStdio(mcp)
	}

	handler := buildMux(flagTransport, mcp, oauthHandler, cfg.DexIssuerURL, grafanaClient, listOrgCount(ctrlCache), cacheAlive)

	// One server for /mcp + /metrics + /healthz + /readyz + /oauth/*.
	// No WriteTimeout because MCP streamable-HTTP / SSE are
	// intentionally long-lived; ReadHeaderTimeout + IdleTimeout still
	// guard against slowloris and lingering keep-alives.
	srv := &http.Server{
		Addr:              flagAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	runListenAndServe(srv, logger, cancel)

	<-shutdownCtx.Done()
	logger.Info("shutdown requested")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		logger.Warn("server drain returned error", "error", err)
	}
	return nil
}

// guardStdioInCluster refuses MCP_TRANSPORT=stdio when KUBERNETES_SERVICE_HOST
// is set, because stdio bypasses OAuth and a misconfigured Deployment would
// silently run auth-free. MCP_ALLOW_STDIO_IN_CLUSTER=true overrides for
// in-cluster integration tests.
func guardStdioInCluster(transport string) error {
	if transport != transportStdio {
		return nil
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return nil
	}
	allow, _ := strconv.ParseBool(os.Getenv("MCP_ALLOW_STDIO_IN_CLUSTER"))
	if allow {
		return nil
	}
	return fmt.Errorf("MCP_TRANSPORT=stdio refused inside Kubernetes (stdio bypasses OAuth); use streamable-http or set MCP_ALLOW_STDIO_IN_CLUSTER=true to override")
}

// parseGrafanaBasicAuth parses GRAFANA_BASIC_AUTH ("user:password") into
// a *url.Userinfo. Empty input returns (nil, nil); a malformed value is
// a hard error.
func parseGrafanaBasicAuth(s string) (*url.Userinfo, error) {
	if s == "" {
		return nil, nil
	}
	user, pass, ok := strings.Cut(s, ":")
	if !ok || user == "" {
		return nil, fmt.Errorf("GRAFANA_BASIC_AUTH must be in the form user:password")
	}
	return url.UserPassword(user, pass), nil
}

// newLogger builds the root slog logger. format is "json" or "text"; debug
// switches the level from Info to Debug.
func newLogger(debug bool, format string) *slog.Logger {
	lvl := slog.LevelInfo
	if debug {
		lvl = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == logFormatJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}
