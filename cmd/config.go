package cmd

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/providers/oidc"

	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
)

// config is the process-wide resolved configuration: env-driven today,
// flag-driven when convenient. Fed by loadConfig at startup.
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
	// are accepted for SSO token-forwarding scenarios. Tokens must still be
	// signed by the configured Dex issuer — this only widens the accepted
	// `aud` claim set. Empty = only tokens minted for this server's own
	// client ID are accepted.
	OAuthTrustedAudiences []string
	// OAuthTrustedRedirectSchemes lists URI schemes (e.g. "cursor",
	// "vscode") accepted for redirect URIs during public client
	// registration without a registration access token. Empty list =
	// only loopback HTTPS is accepted (mcp-oauth default).
	OAuthTrustedRedirectSchemes []string
	ValkeyAddr                  string
	ValkeyPassword              string
	ValkeyTLS                   bool
	GrafanaURL                  string
	GrafanaPublicURL            string
	GrafanaSAToken              string
	GrafanaBasicAuth            string

	// ToolTimeout is the per-tool-call context deadline. Zero disables the
	// middleware entirely; a malformed TOOL_TIMEOUT env value falls back to
	// middleware.DefaultToolTimeout.
	ToolTimeout time.Duration
	// MaxResponseBytes caps tool response TextContent size. Zero disables
	// capping; a malformed TOOL_MAX_RESPONSE_BYTES env value falls back to
	// middleware.DefaultMaxResponseBytes.
	MaxResponseBytes int
}

// loadConfig reads every env var the process needs, validates them, and
// returns a populated *config. Fails fast on missing required vars,
// unparseable URLs, weak encryption keys, or malformed audience lists.
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
		ToolTimeout:                        envDuration("TOOL_TIMEOUT", middleware.DefaultToolTimeout),
		MaxResponseBytes:                   envInt("TOOL_MAX_RESPONSE_BYTES", middleware.DefaultMaxResponseBytes),
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envDuration reads a time.Duration env var. "0" / "0s" yields 0 (disable);
// an unset or malformed value falls back to def.
func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	if v == "0" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

// envInt reads an int env var. 0 is a valid value (typically "disable"); an
// unset or malformed value falls back to def.
func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
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
