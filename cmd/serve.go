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

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
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
	flagTransport   string
	flagMCPAddr     string
	flagMetricsAddr string
	flagDebug       bool
)

func init() {
	serveCmd.Flags().StringVar(&flagTransport, "transport", envOr("MCP_TRANSPORT", transportStreamableHTTP), transportStdio+" | "+transportSSE+" | "+transportStreamableHTTP)
	serveCmd.Flags().StringVar(&flagMCPAddr, "mcp-addr", envOr("MCP_ADDR", ":8080"), "listen address for MCP HTTP transport")
	serveCmd.Flags().StringVar(&flagMetricsAddr, "metrics-addr", envOr("METRICS_ADDR", ":9091"), "listen address for /metrics, /healthz, /readyz, /healthz/detailed")
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
// helpers live in oauth.go / orgregistry.go / mux.go; runServe reads as a
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
		PublicURL: cfg.GrafanaPublicURL,
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

	// authz uses Grafana as the source of truth for org membership. Positive
	// entries cache 30s; negative ones (user-not-found, empty memberships)
	// use a 5s TTL so a mid-SSO-outage failure doesn't lock anyone out for
	// half a minute. LRU-bounded so long-running pods don't leak.
	authorizer, err := authz.NewAuthorizer(k8sOrgRegistry{reader: ctrlCache}, grafanaClient, logger,
		authz.DefaultCacheTTL, authz.DefaultNegativeCacheTTL, authz.DefaultCacheSize)
	if err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	oauthHandler, storeClose, err := buildOAuthHandler(shutdownCtx, cfg, logger)
	if err != nil {
		return err
	}
	defer storeClose()

	// Best-effort OTEL tracing/logs. No-op when no endpoint is configured.
	// Defers stay in runServe so shutdown ordering is explicit.
	shutdownOTEL, err := observability.InitTracing(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer shutdownWithTimeout(shutdownOTEL)
	}
	shutdownOTELLogs, otelLogHandler, err := observability.InitLogging(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel log init failed; continuing without OTLP logs", "error", err)
	} else {
		defer shutdownWithTimeout(shutdownOTELLogs)
	}
	if otelLogHandler != nil {
		logger = slog.New(observability.FanoutHandler(logger.Handler(), otelLogHandler))
	}
	// Emit dev-mode opt-in warnings on the fanout-aware logger so they
	// reach the OTLP log sink, not just stderr.
	if cfg.OAuthAllowInsecureHTTP {
		logger.Warn("OAUTH_ALLOW_INSECURE_HTTP=true — OAuth flows accept plain-HTTP issuers; intended for local dev only")
	}
	if cfg.OAuthAllowPublicClientRegistration {
		logger.Warn("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION=true — /oauth/register is open; restrict in production")
	}

	// Audit trail: JSON-per-tool-call on stderr. Fans out to OTLP when wired
	// so audit + span + metric signals correlate.
	auditHandler := slog.Handler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if otelLogHandler != nil {
		auditHandler = observability.FanoutHandler(auditHandler, otelLogHandler)
	}
	auditLogger := audit.New(auditHandler)

	bridge, err := newUpstreamBridge(authorizer, cfg)
	if err != nil {
		return fmt.Errorf("upstream bridge: %w", err)
	}

	mcp, err := server.New(server.Config{
		Logger:           logger,
		Authorizer:       authorizer,
		Grafana:          grafanaClient,
		Bridge:           bridge,
		Version:          version,
		Audit:            auditLogger,
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

	mcpHandler := buildMCPMux(flagTransport, mcp, oauthHandler)
	obsMux := buildObsMux(version, cfg.DexIssuerURL, grafanaClient, listOrgCount(ctrlCache), cacheAlive)

	startOrgCacheReporter(shutdownCtx, ctrlCache)

	// IdleTimeout closes keep-alives idle past 60s on both servers;
	// WriteTimeout is set only on the obs server because MCP streaming-HTTP
	// / SSE responses are intentionally long-lived.
	mcpServer := &http.Server{
		Addr:              flagMCPAddr,
		Handler:           mcpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	obsServer := &http.Server{
		Addr:              flagMetricsAddr,
		Handler:           obsMux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	runListenAndServe(mcpServer, "MCP", logger, cancel)
	runListenAndServe(obsServer, "observability", logger, cancel)

	<-shutdownCtx.Done()
	logger.Info("shutdown requested")
	runTwoPhaseShutdown(logger, mcpServer, obsServer)
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

// newUpstreamBridge constructs the upstream-mcp-grafana bridge from
// runtime config. APIKey vs BasicAuth are mutually exclusive at config-
// load time (see cmd/config.go) so exactly one of the two will be set.
func newUpstreamBridge(az authz.Authorizer, cfg *config) (*upstream.Bridge, error) {
	br := &upstream.Bridge{
		Authorizer: az,
		GrafanaURL: cfg.GrafanaURL,
		APIKey:     cfg.GrafanaSAToken,
	}
	if cfg.GrafanaBasicAuth != "" {
		user, pass, ok := strings.Cut(cfg.GrafanaBasicAuth, ":")
		if !ok || user == "" {
			return nil, fmt.Errorf("GRAFANA_BASIC_AUTH must be in the form user:password")
		}
		br.BasicAuth = url.UserPassword(user, pass)
	}
	return br, nil
}

// shutdownWithTimeout invokes a provider's Shutdown with a fresh 5s
// background context. Errors are swallowed: shutdown is best-effort.
func shutdownWithTimeout(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = fn(ctx)
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
