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

// Transport names accepted on MCP_TRANSPORT / --transport. mcp-go does not
// export them as constants; values are the user-facing contract documented
// in README.
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

	orgLister, cacheAlive, err := buildOrgCache(shutdownCtx, logger)
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

	// authz uses Grafana as the source of truth for org membership. Positive
	// entries cache 30s; negative ones (user-not-found, empty memberships)
	// use a 5s TTL so a mid-SSO-outage failure doesn't lock anyone out for
	// half a minute.
	authorizer := authz.NewAuthorizer(orgLister, grafanaClient,
		authz.DefaultCacheTTL, authz.DefaultNegativeCacheTTL)

	oauthHandler, storeClose, err := buildOAuthHandler(logger)
	if err != nil {
		return err
	}
	defer storeClose()

	// Best-effort OTEL tracing. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is
	// unset. The cluster log pipeline ships stderr to Loki; we don't run
	// a separate OTLP-logs path.
	shutdownOTEL, err := observability.InitTracing(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer shutdownWithTimeout(shutdownOTEL)
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
		// stdio drives lifecycle from stdin EOF; SIGINT/SIGTERM is intentionally
		// not propagated and shutdownCtx is unused on this path.
		return mcpsrv.ServeStdio(mcp)
	}

	mcpHandler := buildMCPMux(flagTransport, mcp, oauthHandler)
	obsMux := buildObsMux(orgLister, cacheAlive)

	startOrgCacheReporter(shutdownCtx, orgLister)

	// IdleTimeout closes keep-alives idle past 60s on both servers.
	// MCP omits WriteTimeout because streamable-HTTP / SSE responses are
	// intentionally long-lived; the obs server caps writes at 10s. MCP's
	// ReadHeaderTimeout is looser (10s vs 5s) so flaky-network clients can
	// finish the request line; metrics scrapers should not need slack.
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

// shutdownWithTimeout invokes a provider's Shutdown with a fresh 5s
// background context. Errors are swallowed: shutdown is best-effort.
func shutdownWithTimeout(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = fn(ctx)
}

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
