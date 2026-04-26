package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/security"
	"github.com/giantswarm/mcp-oauth/storage"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/giantswarm/mcp-oauth/storage/valkey"
)

// buildOAuthHandler wires the dex provider, the storage backend, the
// mcp-oauth server (with optional encryption-at-rest), and returns a
// handler ready to mount on the MCP mux. storeClose drains the storage
// backend on shutdown.
func buildOAuthHandler(_ context.Context, cfg *config, logger *slog.Logger) (*oauth.Handler, func(), error) {
	dexProvider, err := dex.NewProvider(&dex.Config{
		IssuerURL:    cfg.DexIssuerURL,
		ClientID:     cfg.DexClientID,
		ClientSecret: cfg.DexClientSecret,
		RedirectURL:  cfg.OAuthRedirectURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("dex provider: %w", err)
	}

	tokenStore, clientStore, flowStore, storeClose, err := newOAuthStore(cfg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth store: %w", err)
	}

	// In-cluster memory store loses OAuth state on pod restart and isn't
	// shared across replicas. Warn loudly so operators see it before users do.
	if (cfg.OAuthStorage == "" || cfg.OAuthStorage == "memory") && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("OAUTH_STORAGE=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; use OAUTH_STORAGE=valkey for production")
	}

	srv, err := oauth.NewServer(
		dexProvider,
		tokenStore, clientStore, flowStore,
		&oauth.ServerConfig{
			Issuer:                        cfg.OAuthIssuer,
			AllowInsecureHTTP:             cfg.OAuthAllowInsecureHTTP,
			AllowPublicClientRegistration: cfg.OAuthAllowPublicClientRegistration,
			// Required by MCP CLI clients (Claude Code, mcp-inspector) that
			// register a loopback redirect URI per RFC 8252.
			AllowLocalhostRedirectURIs: true,
			// SSO token forwarding: accept tokens minted for these audiences
			// as if minted for our own client ID. Empty = own-client-only.
			TrustedAudiences: cfg.OAuthTrustedAudiences,
			// Custom redirect schemes (e.g. cursor://, vscode://) accepted
			// during public client registration. mcp-oauth validates per
			// RFC 3986.
			TrustedPublicRegistrationSchemes: cfg.OAuthTrustedRedirectSchemes,
		},
		logger,
	)
	if err != nil {
		storeClose()
		return nil, nil, fmt.Errorf("oauth server: %w", err)
	}
	if cfg.OAuthEncryptionKey != nil {
		enc, err := security.NewEncryptor(cfg.OAuthEncryptionKey)
		if err != nil {
			storeClose()
			return nil, nil, fmt.Errorf("oauth encryptor: %w", err)
		}
		srv.SetEncryptor(enc)
	}
	return oauth.NewHandler(srv, logger), storeClose, nil
}

// newOAuthStore picks the storage backend (memory or valkey) and returns
// three interface views + a teardown. memory.Store and valkey.Store each
// implement TokenStore/ClientStore/FlowStore, so a single instance is
// returned three times.
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
