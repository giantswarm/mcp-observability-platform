package cmd

import (
	"fmt"
	"log/slog"
	"os"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/storage"
)

// OAUTH_* env vars read directly by this package. Upstream owns the rest
// (see oauthconfig.*FromEnvWithPrefix). Names match the helm chart and
// README so an operator can grep the same string across all three.
const (
	envOAuthDexClientID    = "OAUTH_DEX_CLIENT_ID"
	envOAuthDexIssuerURL   = "OAUTH_DEX_ISSUER_URL"
	envOAuthStorageBackend = "OAUTH_STORAGE_BACKEND"
)

// buildOAuthHandler assembles the mcp-oauth handler from OAUTH_* env vars.
// storeClose drains the storage backend on shutdown.
func buildOAuthHandler(logger *slog.Logger) (*oauth.Handler, func(), error) {
	provider, err := oauthconfig.DexFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("dex provider: %w", err)
	}
	// Upstream DexFromEnv does not enforce the dex audience charset on
	// the client ID; check it here so a typo fails startup rather than
	// producing tokens Dex rejects mid-flow.
	if err := dex.ValidateAudience(os.Getenv(envOAuthDexClientID)); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envOAuthDexClientID, err)
	}

	cfg, err := oauthconfig.FromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("oauth config: %w", err)
	}
	// MCP CLI clients (Claude Code, mcp-inspector) register a loopback
	// redirect URI per RFC 8252. The binary always permits it; not an
	// operator knob.
	cfg.AllowLocalhostRedirectURIs = true

	store, storeCloseErr, err := oauthconfig.StorageFromEnv(logger)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth store: %w", err)
	}
	storeClose := func() { _ = storeCloseErr() }

	backend := os.Getenv(envOAuthStorageBackend)
	if (backend == "" || backend == storage.BackendMemory) && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("OAUTH_STORAGE_BACKEND=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; use OAUTH_STORAGE_BACKEND=valkey for production")
	}

	encryptor, err := oauthconfig.NewEncryptorFromEnv()
	if err != nil {
		storeClose()
		return nil, nil, fmt.Errorf("oauth encryptor: %w", err)
	}
	// Valkey persists OAuth state across pod restarts and may live on a
	// shared instance; refuse to start without encryption-at-rest.
	// OAUTH_ALLOW_INSECURE_HTTP=true overrides for local dev (same
	// escape hatch the upstream issuer-scheme check uses).
	if backend == storage.BackendValkey && encryptor == nil && !cfg.AllowInsecureHTTP {
		storeClose()
		return nil, nil, fmt.Errorf("OAUTH_STORAGE_BACKEND=valkey requires OAUTH_ENCRYPTION_KEY (set OAUTH_ALLOW_INSECURE_HTTP=true to override for dev)")
	}

	if cfg.AllowInsecureHTTP {
		logger.Warn("OAUTH_ALLOW_INSECURE_HTTP=true — OAuth flows accept plain-HTTP issuers; intended for local dev only")
	}
	if cfg.AllowPublicClientRegistration {
		logger.Warn("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION=true — /oauth/register is open; restrict in production")
	}

	srv, err := oauth.NewServerWithCombined(provider, store, cfg, logger)
	if err != nil {
		storeClose()
		return nil, nil, fmt.Errorf("oauth server: %w", err)
	}
	if encryptor != nil {
		srv.SetEncryptor(encryptor)
	}
	return oauth.NewHandler(srv, logger), storeClose, nil
}
