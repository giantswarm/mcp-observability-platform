package cmd

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/providers/oidc"
	"github.com/giantswarm/mcp-oauth/security"
	"github.com/giantswarm/mcp-oauth/storage"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/giantswarm/mcp-oauth/storage/valkey"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server",
	RunE:  runServe,
}

// Transport constants for MCP_TRANSPORT / --transport. mcp-go does not
// export these (its own examples use string literals), so we define our
// own and reference them everywhere — matching mcp-kubernetes.
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

	// 60s kubernetes sync period balances cache freshness against API-server
	kubernetesSyncPeriod = 60 * time.Second
)

func init() {
	serveCmd.Flags().StringVar(&flagTransport, "transport", envOr("MCP_TRANSPORT", transportStreamableHTTP), transportStdio+" | "+transportSSE+" | "+transportStreamableHTTP)
	serveCmd.Flags().StringVar(&flagMCPAddr, "mcp-addr", envOr("MCP_ADDR", ":8080"), "listen address for MCP HTTP transport")
	serveCmd.Flags().StringVar(&flagMetricsAddr, "metrics-addr", envOr("METRICS_ADDR", ":9091"), "listen address for /metrics, /healthz, /readyz, /healthz/detailed")
	serveCmd.Flags().BoolVar(&flagDebug, "debug", envBool("DEBUG", false), "enable debug logging")
}

// validateTransport rejects unknown MCP_TRANSPORT values. Extracted so the
// gate is unit-testable without standing up the rest of runServe.
func validateTransport(transport string) error {
	switch transport {
	case transportStdio, transportSSE, transportStreamableHTTP:
		return nil
	default:
		return fmt.Errorf("transport %q is not supported (want one of: %s, %s, %s)", transport, transportStdio, transportSSE, transportStreamableHTTP)
	}
}

func runServe(_ *cobra.Command, _ []string) error {
	logger := newLogger(flagDebug)

	if err := validateTransport(flagTransport); err != nil {
		return err
	}

	shutdownCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Load config from env ---
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.OAuthAllowInsecureHTTP {
		logger.Warn("MCP_OAUTH_ALLOW_INSECURE_HTTP=true — OAuth flows accept plain-HTTP issuers; intended for local dev only")
	}
	if cfg.OAuthAllowPublicClientRegistration {
		logger.Warn("MCP_OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION=true — /oauth/register is open; restrict in production")
	}

	// --- Kubernetes client + informer cache for GrafanaOrganization CRs ---
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(obsv1alpha2.AddToScheme(scheme))

	kubeCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kube config: %w", err)
	}

	ctrlCache, err := ctrlcache.New(kubeCfg, ctrlcache.Options{
		Scheme:     scheme,
		SyncPeriod: &kubernetesSyncPeriod,
	})
	if err != nil {
		return fmt.Errorf("controller-runtime cache: %w", err)
	}

	// Prime the informer for GrafanaOrganization so the list cache is populated on first query.
	if _, err := ctrlCache.GetInformer(shutdownCtx, &obsv1alpha2.GrafanaOrganization{}); err != nil {
		return fmt.Errorf("get informer: %w", err)
	}

	// Track informer liveness so the readiness probe can flip to 503 when
	// ctrlCache.Start exits on a non-canceled error (API server flap, scheme
	// mismatch). Without this, List keeps returning the last-known snapshot
	// and readyz lies.
	var cacheAlive atomic.Bool
	cacheAlive.Store(true)
	go func() {
		if err := ctrlCache.Start(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("controller-runtime cache stopped", "error", err)
			cacheAlive.Store(false)
		}
	}()
	if !ctrlCache.WaitForCacheSync(shutdownCtx) {
		return fmt.Errorf("cache sync timed out")
	}
	logger.Info("GrafanaOrganization cache synced")

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
	// entries are cached 30s; negative ones (user-not-found, empty
	// memberships) use a 5s TTL so a mid-SSO-outage failure doesn't lock
	// anyone out for half a minute. LRU-bounded so long-running pods with
	// many unique callers don't leak.
	authorizer, err := authz.NewAuthorizer(k8sOrgRegistry{reader: ctrlCache}, grafanaClient, logger,
		authz.DefaultCacheTTL, authz.DefaultNegativeCacheTTL, authz.DefaultCacheSize)
	if err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	// --- mcp-oauth ---
	dexProvider, err := dex.NewProvider(&dex.Config{
		IssuerURL:    cfg.DexIssuerURL,
		ClientID:     cfg.DexClientID,
		ClientSecret: cfg.DexClientSecret,
		RedirectURL:  cfg.OAuthRedirectURL,
	})
	if err != nil {
		return fmt.Errorf("dex provider: %w", err)
	}

	tokenStore, clientStore, flowStore, storeClose, err := newOAuthStore(cfg, logger)
	if err != nil {
		return fmt.Errorf("oauth store: %w", err)
	}
	defer storeClose()

	// In-cluster deployments lose OAuth state on pod restart when the memory
	// store is used, and per-pod isolation makes horizontal scaling break
	// mid-flow. Warn loudly so operators notice before users do.
	if cfg.OAuthStorage == "" || cfg.OAuthStorage == "memory" {
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			logger.Warn("OAUTH_STORAGE=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; use OAUTH_STORAGE=valkey for production")
		}
	}

	oauthSrv, err := oauth.NewServer(
		dexProvider,
		tokenStore, clientStore, flowStore,
		&oauth.ServerConfig{
			Issuer:                        cfg.OAuthIssuer,
			AllowInsecureHTTP:             cfg.OAuthAllowInsecureHTTP,
			AllowPublicClientRegistration: cfg.OAuthAllowPublicClientRegistration,
			// Required for MCP CLI clients (Claude Code, mcp-inspector) that
			// register a loopback redirect URI per RFC 8252 (native apps).
			AllowLocalhostRedirectURIs: true,
			// SSO token forwarding: accept tokens minted for these audiences
			// as if they were minted for our own client ID. Tokens still
			// must be signed by the configured Dex issuer — this only
			// widens the accepted `aud` set. Empty = own-client-only.
			TrustedAudiences: cfg.OAuthTrustedAudiences,
			// Extend public client registration to custom schemes beyond
			// the default loopback HTTPS (e.g. `cursor://`, `vscode://`).
			// mcp-oauth validates each scheme per RFC 3986 internally.
			TrustedPublicRegistrationSchemes: cfg.OAuthTrustedRedirectSchemes,
		},
		logger,
	)
	if err != nil {
		return fmt.Errorf("oauth server: %w", err)
	}
	if cfg.OAuthEncryptionKey != nil {
		enc, err := security.NewEncryptor(cfg.OAuthEncryptionKey)
		if err != nil {
			return fmt.Errorf("oauth encryptor: %w", err)
		}
		oauthSrv.SetEncryptor(enc)
	}
	oauthHandler := oauth.NewHandler(oauthSrv, logger)

	// Best-effort OTEL tracing. No-op when no OTEL_EXPORTER_OTLP_ENDPOINT is set.
	shutdownOTEL, err := observability.InitTracing(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer func() {
			sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownOTEL(sc)
		}()
	}

	// Best-effort OTLP logs via the otelslog bridge. When an endpoint is
	// wired every slog record gets trace_id + span_id attached from ctx,
	// so operators can jump from a tool-call trace span to the surrounding
	// log lines in Loki/Grafana without a correlation-ID scheme. nil
	// handler = disabled; both the operator logger and audit stream keep
	// their local sinks unchanged.
	shutdownOTELLogs, otelLogHandler, err := observability.InitLogging(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel log init failed; continuing without OTLP logs", "error", err)
	} else {
		defer func() {
			sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownOTELLogs(sc)
		}()
	}
	if otelLogHandler != nil {
		logger = slog.New(observability.FanoutHandler(logger.Handler(), otelLogHandler))
	}

	// Structured audit trail: one JSON record per tool call on stderr,
	// separate from the debug diagnostic log. Always-on, stable schema,
	// redirect to a dedicated sink at the pod spec when a SIEM ingests it.
	// When OTLP logs are wired the audit stream fans out to both the local
	// JSON sink and OTLP so audit + span + metric signals correlate.
	auditHandler := slog.Handler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if otelLogHandler != nil {
		auditHandler = observability.FanoutHandler(auditHandler, otelLogHandler)
	}
	// Wire a conservative denylist redactor so any tool whose args end up
	// carrying a credential (future additions, upstream param renames) gets
	// masked before emission. The current tool surface takes no secrets, but
	// the denylist is proactive — auditing a leaked secret is worse than the
	// cost of a few extra map reads per call.
	auditLogger := audit.New(auditHandler, audit.WithRedactor(redactSecretArgs))

	// --- MCP server + tools/resources ---
	mcp, err := server.New(server.Config{
		Logger:     logger,
		Authorizer: authorizer,
		Grafana:    grafanaClient,
		Version:    version,
		Audit:      auditLogger,
	})
	if err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	// Stdio transport has no HTTP surface — no OAuth, no /metrics, no
	// readiness probes. mcp-go's ServeStdio owns stdin/stdout and blocks
	// until the client disconnects (or a signal arrives — it installs
	// SIGTERM / SIGINT handlers internally). Callers authenticate as the
	// subprocess user; our authz resolver will reject tool calls that
	// arrive without an OIDC identity in context, so stdio is primarily a
	// developer-loop convenience. Production deploys use streamable-http.
	if flagTransport == transportStdio {
		logger.Info("MCP serving on stdio", "transport", transportStdio)
		logger.Warn("stdio transport bypasses OAuth — tool calls will hit authz errors unless the session provides a caller identity")
		return mcpsrv.ServeStdio(mcp)
	}

	// --- HTTP muxes (streamable-http + sse) ---
	mcpMux := http.NewServeMux()

	// OAuth flow endpoints.
	mcpMux.HandleFunc("/oauth/authorize", oauthHandler.ServeAuthorization)
	mcpMux.HandleFunc("/oauth/callback", oauthHandler.ServeCallback)
	mcpMux.HandleFunc("/oauth/token", oauthHandler.ServeToken)
	mcpMux.HandleFunc("/oauth/revoke", oauthHandler.ServeTokenRevocation)
	mcpMux.HandleFunc("/oauth/register", oauthHandler.ServeClientRegistration)

	// Discovery. Path under resource-metadata matches the MCP endpoint we
	// mount below — different per transport (see the switch).
	resourcePath := "/mcp"
	if flagTransport == transportSSE {
		resourcePath = "/sse"
	}
	oauthHandler.RegisterProtectedResourceMetadataRoutes(mcpMux, resourcePath)
	oauthHandler.RegisterAuthorizationServerMetadataRoutes(mcpMux)

	// Transport-specific MCP handler. Streamable-http is a single path
	// (`/mcp`); SSE is two (`/sse` for event stream, `/message` for
	// client→server posts). Both are gated behind OAuth's ValidateToken.
	switch flagTransport {
	case transportStreamableHTTP:
		mcpMux.Handle("/mcp", oauthHandler.ValidateToken(server.StreamableHTTPHandler(mcp)))
	case transportSSE:
		sseHandler := server.SSEHandler(mcp)
		mcpMux.Handle("/sse", oauthHandler.ValidateToken(sseHandler))
		mcpMux.Handle("/message", oauthHandler.ValidateToken(sseHandler))
	}

	obsMux := http.NewServeMux()

	// Deep readiness: liveness is always-200, readiness probes Grafana +
	// Dex + K8s informer, detailed returns JSON with timings for operators.
	// 2s per-check deadline keeps kubelet probes honest.
	health := server.NewHealthChecker(version, 2*time.Second)
	health.Register("grafana", func(ctx context.Context) (any, error) {
		return nil, grafanaClient.Ping(ctx)
	})
	health.Register("dex", server.HTTPProbe(nil, strings.TrimRight(cfg.DexIssuerURL, "/")+"/.well-known/openid-configuration"))
	health.Register("k8s_cache", func(ctx context.Context) (any, error) {
		// If ctrlCache.Start has exited on a non-canceled error, List would
		// still return the last-known snapshot — which lies. cacheAlive is
		// flipped to false by the Start goroutine on real failure.
		if !cacheAlive.Load() {
			return nil, errors.New("controller-runtime cache stopped")
		}
		var list obsv1alpha2.GrafanaOrganizationList
		if err := ctrlCache.List(ctx, &list); err != nil {
			return nil, err
		}
		return map[string]int{"orgs": len(list.Items)}, nil
	})
	health.RegisterHandlers(obsMux)
	obsMux.Handle("/metrics", observability.MetricsHandler())

	// Keep OrgCacheSize gauge roughly accurate. The informer is event-driven,
	// so polling is only for gauge freshness.
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-shutdownCtx.Done():
				return
			case <-tick.C:
				var list obsv1alpha2.GrafanaOrganizationList
				if err := ctrlCache.List(shutdownCtx, &list); err == nil {
					observability.OrgCacheSize.Set(float64(len(list.Items)))
				}
			}
		}
	}()

	// Wrap the MCP mux with otelhttp so an incoming W3C traceparent becomes
	// a server span, and downstream Grafana spans hang off it. Health and
	// metrics endpoints stay un-instrumented (they'd swamp the traces with
	// kubelet probes).
	mcpHandler := otelhttp.NewHandler(mcpMux, "mcp",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
	mcpServer := &http.Server{Addr: flagMCPAddr, Handler: mcpHandler, ReadHeaderTimeout: 10 * time.Second}
	obsServer := &http.Server{Addr: flagMetricsAddr, Handler: obsMux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		logger.Info("MCP listening", "addr", flagMCPAddr, "transport", flagTransport)
		if err := mcpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("mcp server failed", "error", err)
			cancel()
		}
	}()
	go func() {
		logger.Info("observability listening", "addr", flagMetricsAddr)
		if err := obsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("observability server failed", "error", err)
			cancel()
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown requested")

	// Two-phase shutdown: drain MCP first so in-flight tool calls complete,
	// then drain the observability server. The kubelet's liveness probe and
	// Prometheus scrape keep working while MCP drains — which matters when
	// a slow tool call otherwise shows up as a liveness failure and triggers
	// a SIGKILL mid-drain.
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
	return nil
}

func newLogger(debug bool) *slog.Logger {
	lvl := slog.LevelInfo
	if debug {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

type config struct {
	DexIssuerURL                       string
	DexClientID                        string
	DexClientSecret                    string
	OAuthIssuer                        string
	OAuthRedirectURL                   string
	OAuthAllowInsecureHTTP             bool
	OAuthAllowPublicClientRegistration bool
	OAuthEncryptionKey                 []byte // nil = encryption disabled
	OAuthStorage                       string // "memory" (default) | "valkey"
	// OAuthTrustedAudiences lists additional OAuth client IDs whose tokens
	// are accepted for SSO token-forwarding scenarios (same semantic as
	// mcp-kubernetes / muster). Tokens must still be signed by the
	// configured Dex issuer — this only widens the accepted `aud` claim
	// set. Empty = only tokens minted for this server's own client ID
	// are accepted.
	OAuthTrustedAudiences []string
	// OAuthTrustedRedirectSchemes lists URI schemes (e.g. "cursor",
	// "vscode") accepted for redirect URIs during public client
	// registration without a registration access token. Empty list =
	// only loopback HTTPS is accepted (mcp-oauth default). Each entry
	// is validated by mcp-oauth itself per RFC 3986.
	OAuthTrustedRedirectSchemes []string
	ValkeyAddr                  string
	ValkeyPassword              string
	ValkeyTLS                   bool
	GrafanaURL                  string
	GrafanaPublicURL            string
	GrafanaSAToken              string
	GrafanaBasicAuth            string
}

func loadConfig() (*config, error) {
	c := &config{
		DexIssuerURL:           os.Getenv("DEX_ISSUER_URL"),
		DexClientID:            os.Getenv("DEX_CLIENT_ID"),
		DexClientSecret:        os.Getenv("DEX_CLIENT_SECRET"),
		OAuthIssuer:            os.Getenv("MCP_OAUTH_ISSUER"),
		OAuthRedirectURL:       envOr("MCP_OAUTH_REDIRECT_URL", ""),
		OAuthAllowInsecureHTTP: envBool("MCP_OAUTH_ALLOW_INSECURE_HTTP", false),
		// Public client registration is off by default: letting arbitrary
		// callers register an OAuth client against a production MCP is a
		// standing risk. Opt-in per env for local dev and cluster test
		// deployments where ergonomics beat that risk.
		OAuthAllowPublicClientRegistration: envBool("MCP_OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION", false),
		OAuthStorage:                       strings.ToLower(envOr("OAUTH_STORAGE", "memory")),
		ValkeyAddr:                         os.Getenv("VALKEY_ADDR"),
		ValkeyPassword:                     os.Getenv("VALKEY_PASSWORD"),
		ValkeyTLS:                          envBool("VALKEY_TLS", false),
		GrafanaURL:                         os.Getenv("GRAFANA_URL"),
		GrafanaPublicURL:                   os.Getenv("GRAFANA_PUBLIC_URL"),
		GrafanaSAToken:                     os.Getenv("GRAFANA_SA_TOKEN"),
		GrafanaBasicAuth:                   os.Getenv("GRAFANA_BASIC_AUTH"),
	}
	var missing []string
	for k, v := range map[string]string{
		"DEX_ISSUER_URL":    c.DexIssuerURL,
		"DEX_CLIENT_ID":     c.DexClientID,
		"DEX_CLIENT_SECRET": c.DexClientSecret,
		"MCP_OAUTH_ISSUER":  c.OAuthIssuer,
		"GRAFANA_URL":       c.GrafanaURL,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if c.GrafanaSAToken == "" && c.GrafanaBasicAuth == "" {
		missing = append(missing, "GRAFANA_SA_TOKEN or GRAFANA_BASIC_AUTH")
	}
	if c.OAuthStorage == "valkey" && c.ValkeyAddr == "" {
		missing = append(missing, "VALKEY_ADDR (required when OAUTH_STORAGE=valkey)")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}
	if c.OAuthRedirectURL == "" {
		c.OAuthRedirectURL = c.OAuthIssuer + "/oauth/callback"
	}
	if raw := os.Getenv("MCP_OAUTH_ENCRYPTION_KEY"); raw != "" {
		key, err := decodeEncryptionKey(raw)
		if err != nil {
			return nil, fmt.Errorf("MCP_OAUTH_ENCRYPTION_KEY: %w", err)
		}
		if err := validateEncryptionKeyEntropy(key); err != nil {
			return nil, err
		}
		c.OAuthEncryptionKey = key
	}

	// Trusted audiences + redirect schemes. Audience list is delegated to
	// `dex.ValidateAudiences` (same max-count + charset rules as muster /
	// mcp-kubernetes use for SSO token forwarding). Schemes are passed
	// through; mcp-oauth validates them at server-config time per RFC 3986.
	c.OAuthTrustedAudiences = splitAndTrimCSV(os.Getenv("MCP_OAUTH_TRUSTED_AUDIENCES"))
	if err := dex.ValidateAudiences(c.OAuthTrustedAudiences); err != nil {
		return nil, fmt.Errorf("MCP_OAUTH_TRUSTED_AUDIENCES: %w", err)
	}
	c.OAuthTrustedRedirectSchemes = splitAndTrimCSV(os.Getenv("MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES"))

	// URL + client ID hardening. HTTPS + charset checks are delegated to
	// mcp-oauth's exports. Skipped entirely in dev mode
	// (MCP_OAUTH_ALLOW_INSECURE_HTTP=true) so local http://localhost:5556
	// Dex deployments still work.
	if !c.OAuthAllowInsecureHTTP {
		if err := oidc.ValidateHTTPSURL(c.DexIssuerURL, "DEX_ISSUER_URL"); err != nil {
			return nil, err
		}
		if err := oidc.ValidateHTTPSURL(c.OAuthIssuer, "MCP_OAUTH_ISSUER"); err != nil {
			return nil, err
		}
	}
	if err := dex.ValidateAudience(c.DexClientID); err != nil {
		return nil, fmt.Errorf("DEX_CLIENT_ID: %w", err)
	}

	return c, nil
}

// decodeEncryptionKey accepts either a 64-char hex string or a raw 32-byte
// value and returns the 32-byte key, or an error if neither form matches.
func decodeEncryptionKey(s string) ([]byte, error) {
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("must be 32 raw bytes or 64 hex chars, got %d chars", len(s))
}

// newOAuthStore builds the store described by cfg.OAuthStorage and returns
// three interface views + a teardown. memory.Store and valkey.Store each
// implement TokenStore/ClientStore/FlowStore so a single instance is returned
// three times.
func newOAuthStore(cfg *config, logger *slog.Logger) (
	storage.TokenStore, storage.ClientStore, storage.FlowStore, func(), error,
) {
	switch cfg.OAuthStorage {
	case "", "memory":
		s := memory.New()
		return s, s, s, func() { s.Stop() }, nil
	case "valkey":
		vcfg := valkey.Config{
			Address:  cfg.ValkeyAddr,
			Password: cfg.ValkeyPassword,
			Logger:   logger,
		}
		if cfg.ValkeyTLS {
			vcfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		s, err := valkey.New(vcfg)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("valkey: %w", err)
		}
		return s, s, s, func() { s.Close() }, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unknown OAUTH_STORAGE=%q (want memory|valkey)", cfg.OAuthStorage)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// k8sOrgRegistry adapts a controller-runtime cache to authz.OrgRegistry.
// Lives in cmd/ so the authz package never imports observability-operator
// or controller-runtime — the adapter is the translation boundary between
// K8s CR shapes and the domain Organization.
type k8sOrgRegistry struct{ reader ctrlclient.Reader }

func (k k8sOrgRegistry) List(ctx context.Context) ([]authz.Organization, error) {
	var list obsv1alpha2.GrafanaOrganizationList
	if err := k.reader.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]authz.Organization, len(list.Items))
	for i := range list.Items {
		cr := &list.Items[i]
		tenants := make([]authz.Tenant, 0, len(cr.Spec.Tenants))
		for _, t := range cr.Spec.Tenants {
			types := make([]authz.TenantType, 0, len(t.Types))
			for _, tt := range t.Types {
				types = append(types, authz.TenantType(tt))
			}
			tenants = append(tenants, authz.Tenant{Name: string(t.Name), Types: types})
		}
		datasources := make([]authz.Datasource, 0, len(cr.Status.DataSources))
		for _, ds := range cr.Status.DataSources {
			datasources = append(datasources, authz.Datasource{ID: ds.ID, Name: ds.Name})
		}
		out[i] = authz.Organization{
			Name:        cr.Name,
			DisplayName: cr.Spec.DisplayName,
			OrgID:       cr.Status.OrgID,
			Tenants:     tenants,
			Datasources: datasources,
		}
	}
	return out, nil
}

// secretArgKeys is the denylist of tool-argument names whose values must never
// appear in audit records. Matched case-insensitively against the full key
// and as a substring (so "api_key", "auth_token", "bearerToken" all hit).
// Additions are safe (redaction is a one-way projection); removals need a
// security review.
var secretArgKeys = []string{
	"token",
	"apikey", "api_key",
	"authorization",
	"cookie",
	"bearer",
	"password", "passwd",
	"secret",
	"credential", "credentials",
}

// redactSecretArgs replaces values of any key containing a secret-like
// substring with "[REDACTED]". Operates on the defensive copy audit.Logger
// gives us; safe to mutate in place.
func redactSecretArgs(args map[string]any) map[string]any {
	for k := range args {
		lk := strings.ToLower(k)
		for _, needle := range secretArgKeys {
			if strings.Contains(lk, needle) {
				args[k] = "[REDACTED]"
				break
			}
		}
	}
	return args
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
