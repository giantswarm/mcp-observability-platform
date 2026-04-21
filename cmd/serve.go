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
	"syscall"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/security"
	"github.com/giantswarm/mcp-oauth/storage"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/giantswarm/mcp-oauth/storage/valkey"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
	"github.com/giantswarm/mcp-observability-platform/internal/tracing"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server",
	RunE:  runServe,
}

var (
	flagTransport   string
	flagMCPAddr     string
	flagMetricsAddr string
	flagDebug       bool
)

func init() {
	serveCmd.Flags().StringVar(&flagTransport, "transport", envOr("MCP_TRANSPORT", "streamable-http"), "stdio | sse | streamable-http")
	serveCmd.Flags().StringVar(&flagMCPAddr, "mcp-addr", envOr("MCP_ADDR", ":8080"), "listen address for MCP HTTP transport")
	serveCmd.Flags().StringVar(&flagMetricsAddr, "metrics-addr", envOr("METRICS_ADDR", ":9091"), "listen address for /metrics, /healthz, /readyz")
	serveCmd.Flags().BoolVar(&flagDebug, "debug", envBool("DEBUG", false), "enable debug logging")
}

func runServe(_ *cobra.Command, _ []string) error {
	logger := newLogger(flagDebug)

	shutdownCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Load config from env ---
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// --- Kubernetes client + informer cache for GrafanaOrganization CRs ---
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(obsv1alpha2.AddToScheme(scheme))

	kubeCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kube config: %w", err)
	}

	resync := 60 * time.Second
	ctrlCache, err := ctrlcache.New(kubeCfg, ctrlcache.Options{
		Scheme:     scheme,
		SyncPeriod: &resync,
	})
	if err != nil {
		return fmt.Errorf("controller-runtime cache: %w", err)
	}

	// Prime the informer for GrafanaOrganization so the list cache is populated on first query.
	if _, err := ctrlCache.GetInformer(shutdownCtx, &obsv1alpha2.GrafanaOrganization{}); err != nil {
		return fmt.Errorf("get informer: %w", err)
	}

	go func() {
		if err := ctrlCache.Start(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("controller-runtime cache stopped", "error", err)
		}
	}()
	if !ctrlCache.WaitForCacheSync(shutdownCtx) {
		return fmt.Errorf("cache sync timed out")
	}
	logger.Info("GrafanaOrganization cache synced")

	gfClient, err := grafana.New(grafana.Config{
		URL:       cfg.GrafanaURL,
		PublicURL: cfg.GrafanaPublicURL,
		Token:     cfg.GrafanaSAToken,
		BasicAuth: cfg.GrafanaBasicAuth,
	})
	if err != nil {
		return fmt.Errorf("grafana client: %w", err)
	}
	if err := gfClient.VerifyServerAdmin(shutdownCtx); err != nil {
		return fmt.Errorf(
			"Grafana credential is not server-admin (cannot list all orgs). "+
				"Use a server-admin SA token, or set GRAFANA_BASIC_AUTH=admin:password: %w", err)
	}
	logger.Info("Grafana server-admin credential verified")

	// authz uses Grafana as the source of truth for org membership. 30s
	// per-caller cache to avoid hitting /api/users/lookup on every MCP call.
	resolver := authz.NewResolver(ctrlCache, grafanaAuthz{c: gfClient}, logger, 30*time.Second)

	// Grafana client is built during resolver construction above.

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

	// Best-effort OTEL init. No-op when no OTEL_EXPORTER_OTLP_ENDPOINT is set.
	shutdownOTEL, err := tracing.Init(shutdownCtx, "mcp-observability-platform", version)
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer func() {
			sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownOTEL(sc)
		}()
	}

	// --- MCP server + tools/resources ---
	mcpHTTP, err := server.New(server.Config{
		Logger:   logger,
		Resolver: resolver,
		Grafana:  gfClient,
		Version:  version,
	})
	if err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	// --- HTTP muxes ---
	mcpMux := http.NewServeMux()

	// OAuth flow endpoints.
	mcpMux.HandleFunc("/oauth/authorize", oauthHandler.ServeAuthorization)
	mcpMux.HandleFunc("/oauth/callback", oauthHandler.ServeCallback)
	mcpMux.HandleFunc("/oauth/token", oauthHandler.ServeToken)
	mcpMux.HandleFunc("/oauth/revoke", oauthHandler.ServeTokenRevocation)
	mcpMux.HandleFunc("/oauth/register", oauthHandler.ServeClientRegistration)

	// Discovery.
	oauthHandler.RegisterProtectedResourceMetadataRoutes(mcpMux, "/mcp")
	oauthHandler.RegisterAuthorizationServerMetadataRoutes(mcpMux)

	// MCP handler gated by OAuth.
	mcpMux.Handle("/mcp", oauthHandler.ValidateToken(mcpHTTP))

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	obsMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
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

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer drainCancel()
	_ = mcpServer.Shutdown(drainCtx)
	_ = obsServer.Shutdown(drainCtx)
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
	ValkeyAddr                         string
	ValkeyPassword                     string
	ValkeyTLS                          bool
	GrafanaURL                         string
	GrafanaPublicURL                   string
	GrafanaSAToken                     string
	GrafanaBasicAuth                   string
}

func loadConfig() (*config, error) {
	c := &config{
		DexIssuerURL:                       os.Getenv("DEX_ISSUER_URL"),
		DexClientID:                        os.Getenv("DEX_CLIENT_ID"),
		DexClientSecret:                    os.Getenv("DEX_CLIENT_SECRET"),
		OAuthIssuer:                        os.Getenv("MCP_OAUTH_ISSUER"),
		OAuthRedirectURL:                   envOr("MCP_OAUTH_REDIRECT_URL", ""),
		OAuthAllowInsecureHTTP:             envBool("MCP_OAUTH_ALLOW_INSECURE_HTTP", false),
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
		c.OAuthEncryptionKey = key
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

// grafanaAuthz adapts *grafana.Client to authz.GrafanaOrgLookup without
// leaking grafana-package types into authz (which would create an import
// cycle since grafana already imports observability/metrics, and we want
// authz to stay K8s-only).
type grafanaAuthz struct{ c *grafana.Client }

func (g grafanaAuthz) LookupUserID(ctx context.Context, loginOrEmail string) (int64, bool, error) {
	u, err := g.c.LookupUser(ctx, loginOrEmail)
	if err != nil {
		return 0, false, err
	}
	if u == nil {
		return 0, false, nil
	}
	return u.ID, true, nil
}

func (g grafanaAuthz) UserOrgs(ctx context.Context, userID int64) ([]authz.Membership, error) {
	ms, err := g.c.UserOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]authz.Membership, 0, len(ms))
	for _, m := range ms {
		out = append(out, authz.Membership{OrgID: m.OrgID, Role: m.Role})
	}
	return out, nil
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
