package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
)

// clearEnv unsets every var loadConfig looks at so a test starts from a
// known state — Go's testing framework isolates via t.Setenv, but loadConfig
// calls os.Getenv directly on many vars and we need a blank slate.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DEX_ISSUER_URL", "DEX_CLIENT_ID", "DEX_CLIENT_SECRET",
		"MCP_OAUTH_ISSUER", "MCP_OAUTH_REDIRECT_URL",
		"MCP_OAUTH_ALLOW_INSECURE_HTTP", "MCP_OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION",
		"MCP_OAUTH_ENCRYPTION_KEY", "MCP_OAUTH_TRUSTED_AUDIENCES",
		"MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES",
		"OAUTH_STORAGE", "VALKEY_ADDR", "VALKEY_PASSWORD", "VALKEY_TLS",
		"GRAFANA_URL", "GRAFANA_PUBLIC_URL", "GRAFANA_SA_TOKEN", "GRAFANA_BASIC_AUTH",
		"TOOL_TIMEOUT", "TOOL_MAX_RESPONSE_BYTES",
	} {
		t.Setenv(k, "")
	}
}

// setMinimalValid populates just enough env to get past the required-var
// guard in loadConfig. Tests layer additional vars on top of this baseline.
func setMinimalValid(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("MCP_OAUTH_ALLOW_INSECURE_HTTP", "true") // skip HTTPS URL validation in tests
	t.Setenv("DEX_ISSUER_URL", "http://dex.local")
	t.Setenv("DEX_CLIENT_ID", "mcp")
	t.Setenv("DEX_CLIENT_SECRET", "secret")
	t.Setenv("MCP_OAUTH_ISSUER", "http://mcp.local")
	t.Setenv("GRAFANA_URL", "http://grafana.local")
	t.Setenv("GRAFANA_SA_TOKEN", "token")
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	clearEnv(t)
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for missing required env vars")
	}
	for _, want := range []string{"DEX_ISSUER_URL", "DEX_CLIENT_ID", "DEX_CLIENT_SECRET", "MCP_OAUTH_ISSUER", "GRAFANA_URL", "GRAFANA_SA_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-var error should list %q: %v", want, err)
		}
	}
}

func TestLoadConfig_ValkeyStorageNeedsAddr(t *testing.T) {
	setMinimalValid(t)
	t.Setenv("OAUTH_STORAGE", "valkey")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "VALKEY_ADDR") {
		t.Fatalf("expected valkey-needs-addr error, got: %v", err)
	}
}

func TestLoadConfig_DefaultsToolTimeoutAndMaxResponseBytes(t *testing.T) {
	setMinimalValid(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ToolTimeout != middleware.DefaultToolTimeout {
		t.Errorf("ToolTimeout = %s, want %s", cfg.ToolTimeout, middleware.DefaultToolTimeout)
	}
	if cfg.MaxResponseBytes != middleware.DefaultMaxResponseBytes {
		t.Errorf("MaxResponseBytes = %d, want %d", cfg.MaxResponseBytes, middleware.DefaultMaxResponseBytes)
	}
}

func TestLoadConfig_RedirectURLDefault(t *testing.T) {
	setMinimalValid(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// When MCP_OAUTH_REDIRECT_URL is unset, it defaults to issuer + /oauth/callback.
	want := "http://mcp.local/oauth/callback"
	if cfg.OAuthRedirectURL != want {
		t.Errorf("OAuthRedirectURL = %q, want %q", cfg.OAuthRedirectURL, want)
	}
}

func TestLoadConfig_WeakEncryptionKeyRejected(t *testing.T) {
	setMinimalValid(t)
	// 32 bytes of a single repeated character — zero-entropy, catastrophic.
	t.Setenv("MCP_OAUTH_ENCRYPTION_KEY", strings.Repeat("a", 32))
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "low entropy") {
		t.Fatalf("expected low-entropy rejection, got: %v", err)
	}
}

func TestEnvDuration(t *testing.T) {
	cases := []struct {
		name, env string
		def, want time.Duration
	}{
		{"unset", "", 30 * time.Second, 30 * time.Second},
		{"zero disables", "0", 30 * time.Second, 0},
		{"valid", "5m", 30 * time.Second, 5 * time.Minute},
		{"malformed falls back", "not-a-duration", 30 * time.Second, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			if got := envDuration("X", c.def); got != c.want {
				t.Errorf("envDuration(%q, %s) = %s, want %s", c.env, c.def, got, c.want)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name, env string
		def, want int
	}{
		{"unset", "", 128 * 1024, 128 * 1024},
		{"zero valid", "0", 128 * 1024, 0},
		{"valid", "1024", 128 * 1024, 1024},
		{"malformed falls back", "not-a-number", 128 * 1024, 128 * 1024},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			if got := envInt("X", c.def); got != c.want {
				t.Errorf("envInt(%q, %d) = %d, want %d", c.env, c.def, got, c.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		name, env string
		def, want bool
	}{
		{"unset default false", "", false, false},
		{"unset default true", "", true, true},
		{"true", "true", false, true},
		{"false", "false", true, false},
		{"1", "1", false, true},
		{"malformed falls back to default", "not-a-bool", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			if got := envBool("X", c.def); got != c.want {
				t.Errorf("envBool(%q, %v) = %v, want %v", c.env, c.def, got, c.want)
			}
		})
	}
}
