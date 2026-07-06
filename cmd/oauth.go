package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	oauthserver "github.com/giantswarm/mcp-oauth/server"
	"github.com/giantswarm/mcp-oauth/storage"
)

// OAUTH_* env vars read directly by this package. Upstream owns the rest
// (see oauthconfig.*FromEnvWithPrefix). Names match the helm chart and
// README so an operator can grep the same string across all three.
const (
	envOAuthDexClientID    = "OAUTH_DEX_CLIENT_ID"
	envOAuthDexIssuerURL   = "OAUTH_DEX_ISSUER_URL"
	envOAuthStorageBackend = "OAUTH_STORAGE_BACKEND"
	envOAuthTrustedIssuers = "OAUTH_TRUSTED_ISSUERS"
)

// trustedIssuerConfig is the JSON shape of a single OAUTH_TRUSTED_ISSUERS
// entry. It mirrors the subset of mcp-oauth's TrustedIssuer a resource server
// needs. subjectClaim remaps the caller subject to another claim's value at
// validation time (e.g. "email", so the Grafana user lookup gets the handle
// Grafana provisions OAuth users by). acceptedTypHeaders defaults to
// ["at+jwt"] (RFC 9068) upstream; set [""] to accept tokens with no typ
// header (e.g. Kubernetes ServiceAccount tokens).
type trustedIssuerConfig struct {
	Issuer                  string            `json:"issuer"`
	JwksURL                 string            `json:"jwksURL"`
	SubjectClaim            string            `json:"subjectClaim,omitempty"`
	AllowedAudiences        []string          `json:"allowedAudiences,omitempty"`
	AllowedClaims           map[string]string `json:"allowedClaims,omitempty"`
	AcceptedTypHeaders      []string          `json:"acceptedTypHeaders,omitempty"`
	AllowPrivateIPJWKS      bool              `json:"allowPrivateIPJWKS,omitempty"`
	AllowPrivateIPJWKSHosts []string          `json:"allowPrivateIPJWKSHosts,omitempty"`
}

// parseTrustedIssuers parses OAUTH_TRUSTED_ISSUERS, a JSON array of issuer
// objects, into mcp-oauth TrustedIssuer values. Empty input yields nil. Each
// entry requires a non-empty issuer and jwksURL.
func parseTrustedIssuers(raw string) ([]oauthserver.TrustedIssuer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []trustedIssuerConfig
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", envOAuthTrustedIssuers, err)
	}
	issuers := make([]oauthserver.TrustedIssuer, 0, len(entries))
	for i, e := range entries {
		if e.Issuer == "" || e.JwksURL == "" {
			return nil, fmt.Errorf("%s[%d]: issuer and jwksURL are required", envOAuthTrustedIssuers, i)
		}
		issuers = append(issuers, oauthserver.TrustedIssuer{
			Issuer:                  e.Issuer,
			JwksURL:                 e.JwksURL,
			SubjectClaim:            e.SubjectClaim,
			AllowedAudiences:        e.AllowedAudiences,
			AllowedClaims:           e.AllowedClaims,
			AcceptedTypHeaders:      e.AcceptedTypHeaders,
			AllowPrivateIPJWKS:      e.AllowPrivateIPJWKS,
			AllowPrivateIPJWKSHosts: e.AllowPrivateIPJWKSHosts,
		})
	}
	return issuers, nil
}

// buildOAuthHandler assembles the mcp-oauth handler from OAUTH_* env vars.
// storeClose drains the storage backend on shutdown.
func buildOAuthHandler(logger *slog.Logger) (*handler.Handler, func(), error) {
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

	encryptor, err := oauthconfig.NewEncryptorFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("oauth encryptor: %w", err)
	}

	backend := os.Getenv(envOAuthStorageBackend)
	// Valkey persists OAuth state across pod restarts and may live on a
	// shared instance; refuse to start without encryption-at-rest.
	// OAUTH_ALLOW_INSECURE_HTTP=true overrides for local dev (same
	// escape hatch the upstream issuer-scheme check uses).
	if backend == storage.BackendValkey && encryptor == nil && !cfg.AllowInsecureHTTP {
		return nil, nil, fmt.Errorf("OAUTH_STORAGE_BACKEND=valkey requires OAUTH_ENCRYPTION_KEY (set OAUTH_ALLOW_INSECURE_HTTP=true to override for dev)")
	}

	store, storeCloseErr, err := oauthconfig.StorageFromEnv(encryptor, nil, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth store: %w", err)
	}
	storeClose := func() { _ = storeCloseErr() }

	if (backend == "" || backend == storage.BackendMemory) && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("OAUTH_STORAGE_BACKEND=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; use OAUTH_STORAGE_BACKEND=valkey for production")
	}

	if cfg.AllowInsecureHTTP {
		logger.Warn("OAUTH_ALLOW_INSECURE_HTTP=true — OAuth flows accept plain-HTTP issuers; intended for local dev only")
	}
	if cfg.AllowPublicClientRegistration {
		logger.Warn("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION=true — /oauth/register is open; restrict in production")
	}

	// Trusted external issuers (muster) whose Bearer JWTs are accepted at
	// /mcp alongside Dex, for on-behalf-of traffic carrying muster-issued
	// tokens.
	issuers, err := parseTrustedIssuers(os.Getenv(envOAuthTrustedIssuers))
	if err != nil {
		storeClose()
		return nil, nil, err
	}
	var opts []oauth.ServerOption
	if len(issuers) > 0 {
		opts = append(opts, oauthserver.WithTrustedIssuers(issuers))
	}

	srv, err := oauth.NewServerWithCombined(provider, store, cfg, logger, opts...)
	if err != nil {
		storeClose()
		return nil, nil, fmt.Errorf("oauth server: %w", err)
	}
	return handler.New(srv, logger), storeClose, nil
}
