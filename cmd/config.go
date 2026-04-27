package cmd

import (
	"encoding/base64"
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

// OAUTH_STORAGE values accepted by loadConfig / newOAuthStore.
const (
	oauthStorageMemory = "memory"
	oauthStorageValkey = "valkey"
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
	// middleware entirely; a malformed TOOL_TIMEOUT env value fails startup.
	ToolTimeout time.Duration
	// MaxResponseBytes caps tool response TextContent size. Zero disables
	// capping; a malformed TOOL_MAX_RESPONSE_BYTES env value fails startup.
	MaxResponseBytes int

	// Debug enables debug-level logging. Env: DEBUG; --debug flag overrides.
	Debug bool

	// LogFormat selects the slog handler: "json" or "text". Defaults to "json"
	// when KUBERNETES_SERVICE_HOST is set (log aggregators parse structured
	// fields), else "text" for readable local output. LOG_FORMAT overrides.
	LogFormat string
}

// loadConfig reads every env var the process needs, validates them, and
// returns a populated *config. Fails fast on missing required vars,
// unparseable URLs, weak encryption keys, or malformed audience lists.
func loadConfig() (*config, error) {
	allowInsecureHTTP, err := envBool("OAUTH_ALLOW_INSECURE_HTTP", false)
	if err != nil {
		return nil, err
	}
	// Public client registration is off by default: letting arbitrary
	// callers register an OAuth client against a production MCP is a
	// standing risk. Opt-in per env for local dev and cluster test
	// deployments where ergonomics beat that risk.
	allowPublicClientReg, err := envBool("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION", false)
	if err != nil {
		return nil, err
	}
	valkeyTLS, err := envBool("VALKEY_TLS", false)
	if err != nil {
		return nil, err
	}
	debug, err := envBool("DEBUG", false)
	if err != nil {
		return nil, err
	}
	logFormat, err := resolveLogFormat()
	if err != nil {
		return nil, err
	}
	toolTimeout, err := envDuration("TOOL_TIMEOUT", middleware.DefaultToolTimeout)
	if err != nil {
		return nil, err
	}
	maxResponseBytes, err := envInt("TOOL_MAX_RESPONSE_BYTES", middleware.DefaultMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	c := &config{
		DexIssuerURL:                       os.Getenv("DEX_ISSUER_URL"),
		DexClientID:                        os.Getenv("DEX_CLIENT_ID"),
		DexClientSecret:                    os.Getenv("DEX_CLIENT_SECRET"),
		OAuthIssuer:                        os.Getenv("OAUTH_ISSUER"),
		OAuthRedirectURL:                   envOr("OAUTH_REDIRECT_URL", ""),
		OAuthAllowInsecureHTTP:             allowInsecureHTTP,
		OAuthAllowPublicClientRegistration: allowPublicClientReg,
		OAuthStorage:                       strings.ToLower(envOr("OAUTH_STORAGE", oauthStorageMemory)),
		ValkeyAddr:                         os.Getenv("VALKEY_ADDR"),
		ValkeyPassword:                     os.Getenv("VALKEY_PASSWORD"),
		ValkeyTLS:                          valkeyTLS,
		GrafanaURL:                         os.Getenv("GRAFANA_URL"),
		GrafanaPublicURL:                   os.Getenv("GRAFANA_PUBLIC_URL"),
		GrafanaSAToken:                     os.Getenv("GRAFANA_SA_TOKEN"),
		GrafanaBasicAuth:                   os.Getenv("GRAFANA_BASIC_AUTH"),
		ToolTimeout:                        toolTimeout,
		MaxResponseBytes:                   maxResponseBytes,
		Debug:                              debug,
		LogFormat:                          logFormat,
	}
	var missing []string
	for k, v := range map[string]string{
		"DEX_ISSUER_URL":    c.DexIssuerURL,
		"DEX_CLIENT_ID":     c.DexClientID,
		"DEX_CLIENT_SECRET": c.DexClientSecret,
		"OAUTH_ISSUER":      c.OAuthIssuer,
		"GRAFANA_URL":       c.GrafanaURL,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if c.GrafanaSAToken == "" && c.GrafanaBasicAuth == "" {
		missing = append(missing, "GRAFANA_SA_TOKEN or GRAFANA_BASIC_AUTH")
	}
	if c.GrafanaSAToken != "" && c.GrafanaBasicAuth != "" {
		return nil, fmt.Errorf("GRAFANA_SA_TOKEN and GRAFANA_BASIC_AUTH are mutually exclusive — set one and unset the other")
	}
	if c.OAuthStorage == oauthStorageValkey && c.ValkeyAddr == "" {
		missing = append(missing, "VALKEY_ADDR (required when OAUTH_STORAGE=valkey)")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}
	if c.OAuthRedirectURL == "" {
		c.OAuthRedirectURL = c.OAuthIssuer + "/oauth/callback"
	}
	if raw := os.Getenv("OAUTH_ENCRYPTION_KEY"); raw != "" {
		key, err := decodeEncryptionKey(raw)
		if err != nil {
			return nil, fmt.Errorf("OAUTH_ENCRYPTION_KEY: %w", err)
		}
		if err := validateEncryptionKeyEntropy(key); err != nil {
			return nil, err
		}
		c.OAuthEncryptionKey = key
	}
	// Valkey-backed OAuth state (tokens, codes, PKCE state) persists across
	// pod restarts and may live on a shared instance. Refuse to start without
	// encryption-at-rest; OAUTH_ALLOW_INSECURE_HTTP=true overrides for dev.
	if c.OAuthStorage == oauthStorageValkey && c.OAuthEncryptionKey == nil && !c.OAuthAllowInsecureHTTP {
		return nil, fmt.Errorf("OAUTH_STORAGE=valkey requires OAUTH_ENCRYPTION_KEY (set OAUTH_ALLOW_INSECURE_HTTP=true to override for dev)")
	}

	// Trusted audiences + redirect schemes. Audience list is delegated to
	// `dex.ValidateAudiences` (same max-count + charset rules as muster /
	// mcp-kubernetes use for SSO token forwarding). Schemes are passed
	// through; mcp-oauth validates them at server-config time per RFC 3986.
	c.OAuthTrustedAudiences = splitAndTrimCSV(os.Getenv("OAUTH_TRUSTED_AUDIENCES"))
	if err := dex.ValidateAudiences(c.OAuthTrustedAudiences); err != nil {
		return nil, fmt.Errorf("OAUTH_TRUSTED_AUDIENCES: %w", err)
	}
	c.OAuthTrustedRedirectSchemes = splitAndTrimCSV(os.Getenv("OAUTH_TRUSTED_REDIRECT_SCHEMES"))

	// URL + client ID hardening. HTTPS + charset checks are delegated to
	// mcp-oauth's exports. Skipped entirely in dev mode
	// (OAUTH_ALLOW_INSECURE_HTTP=true) so local http://localhost:5556
	// Dex deployments still work.
	if !c.OAuthAllowInsecureHTTP {
		if err := oidc.ValidateHTTPSURL(c.DexIssuerURL, "DEX_ISSUER_URL"); err != nil {
			return nil, err
		}
		if err := oidc.ValidateHTTPSURL(c.OAuthIssuer, "OAUTH_ISSUER"); err != nil {
			return nil, err
		}
	}
	if err := dex.ValidateAudience(c.DexClientID); err != nil {
		return nil, fmt.Errorf("DEX_CLIENT_ID: %w", err)
	}

	return c, nil
}

// decodeEncryptionKey accepts a 64-char hex string or a 44-char standard
// base64 string (with a single "=" pad), both decoding to 32 bytes. Raw
// 32-byte input is no longer accepted — see README for `openssl rand -hex 32`.
func decodeEncryptionKey(s string) ([]byte, error) {
	switch len(s) {
	case 64:
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("64-char value is not valid hex: %w", err)
		}
		return b, nil
	case 44:
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("44-char value is not valid base64: %w", err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("base64 decoded to %d bytes, want 32", len(b))
		}
		return b, nil
	default:
		return nil, fmt.Errorf("must be 64 hex chars or 44 base64 chars, got %d chars", len(s))
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// LogFormat values accepted by newLogger.
const (
	logFormatJSON = "json"
	logFormatText = "text"
)

// resolveLogFormat picks the slog handler based on LOG_FORMAT, or infers one
// from KUBERNETES_SERVICE_HOST when LOG_FORMAT is unset. An unknown
// LOG_FORMAT value is a hard error so operators see the typo at startup
// instead of silently falling back to text on a JSON-parsed log pipeline.
func resolveLogFormat() (string, error) {
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		switch strings.ToLower(v) {
		case logFormatJSON:
			return logFormatJSON, nil
		case logFormatText:
			return logFormatText, nil
		default:
			return "", fmt.Errorf("LOG_FORMAT=%q: want %q or %q", v, logFormatJSON, logFormatText)
		}
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return logFormatJSON, nil
	}
	return logFormatText, nil
}

// envDuration reads a time.Duration env var. "" returns def; "0"/"0s" returns
// 0 (the conventional "disable this feature" marker); an unparseable value is
// a hard error so loadConfig fails fast rather than silently running with the
// default.
func envDuration(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	if v == "0" || v == "0s" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: not a duration (%w)", k, v, err)
	}
	return d, nil
}

// envInt reads an int env var. "" returns def; 0 is a valid parsed value
// (typically "disable"); an unparseable value is a hard error.
func envInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: not an integer (%w)", k, v, err)
	}
	return n, nil
}

// envBool reads a bool env var. Accepts strconv.ParseBool forms
// (true/false/1/0/t/f/TRUE/FALSE/True/False). "" returns def; an unparseable
// value is a hard error so a typo like `DEBUG=yes` fails startup instead of
// silently becoming false.
func envBool(k string, def bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: not a bool (want true|false|1|0)", k, v)
	}
	return b, nil
}
