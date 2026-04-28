package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers/dex"
)

// buildOAuthHandler wires the dex provider, the storage backend, the
// mcp-oauth server, and optional encryption-at-rest. Every env-var read
// is delegated to upstream oauthconfig (OAUTH_* prefix) — see its package
// doc for the full variable list and the *_FILE secret-mount convention.
func buildOAuthHandler(_ context.Context, logger *slog.Logger) (*oauth.Handler, func() error, error) {
	provider, err := oauthconfig.DexFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("dex provider: %w", err)
	}

	// Keep storage env vars under OAUTH_* — the Valkey instance is for
	// OAuth state, not a generic app store. Bare STORAGE_BACKEND /
	// VALKEY_* would mislead operators wiring a future second Valkey.
	store, storeClose, err := oauthconfig.StorageFromEnvWithPrefix("OAUTH_", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth store: %w", err)
	}

	// In-cluster memory store loses OAuth state on pod restart and isn't
	// shared across replicas. Warn loudly so operators see it before users do.
	backend := os.Getenv("OAUTH_STORAGE_BACKEND")
	if (backend == "" || backend == "memory") && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("OAUTH_STORAGE_BACKEND=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; use OAUTH_STORAGE_BACKEND=valkey for production")
	}

	srvCfg, err := oauthconfig.FromEnv()
	if err != nil {
		_ = storeClose()
		return nil, nil, fmt.Errorf("oauth config: %w", err)
	}
	// oauthconfig.FromEnv only splits OAUTH_TRUSTED_AUDIENCES; the upstream
	// server filters invalid entries with a warning, but a typo in a
	// production deployment should be a startup error rather than silent
	// SSO breakage. Hard-fail here.
	if err := dex.ValidateAudiences(srvCfg.TrustedAudiences); err != nil {
		_ = storeClose()
		return nil, nil, fmt.Errorf("OAUTH_TRUSTED_AUDIENCES: %w", err)
	}

	if srvCfg.AllowInsecureHTTP {
		logger.Warn("OAUTH_ALLOW_INSECURE_HTTP=true — OAuth flows accept plain-HTTP issuers; intended for local dev only")
	}
	if srvCfg.AllowPublicClientRegistration {
		logger.Warn("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION=true — /oauth/register is open; restrict in production")
	}

	srv, err := oauth.NewServerWithCombined(provider, store, srvCfg, logger)
	if err != nil {
		_ = storeClose()
		return nil, nil, fmt.Errorf("oauth server: %w", err)
	}

	enc, err := oauthconfig.NewEncryptorFromEnv()
	if err != nil {
		_ = storeClose()
		return nil, nil, fmt.Errorf("oauth encryptor: %w", err)
	}
	if enc != nil {
		srv.SetEncryptor(enc)
	}
	// Valkey-backed OAuth state (tokens, codes, PKCE state) persists across
	// pod restarts and may live on a shared instance. Refuse to start without
	// encryption-at-rest; OAUTH_ALLOW_INSECURE_HTTP=true overrides for dev.
	if backend == "valkey" && enc == nil && !srvCfg.AllowInsecureHTTP {
		_ = storeClose()
		return nil, nil, fmt.Errorf("OAUTH_STORAGE_BACKEND=valkey requires OAUTH_ENCRYPTION_KEY (set OAUTH_ALLOW_INSECURE_HTTP=true to override for dev)")
	}
	return oauth.NewHandler(srv, logger), storeClose, nil
}
