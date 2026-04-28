package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
)

// clearEnv unsets every var loadConfig looks at so a test starts from a
// known state — t.Setenv isolates per-test, but we set everything to "" so
// the sequence of os.Getenv calls inside loadConfig sees a blank slate.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GRAFANA_URL", "GRAFANA_SA_TOKEN", "GRAFANA_BASIC_AUTH",
		"TOOL_TIMEOUT", "TOOL_MAX_RESPONSE_BYTES",
		"DEBUG", "LOG_FORMAT", "KUBERNETES_SERVICE_HOST",
	} {
		t.Setenv(k, "")
	}
}

// setMinimalValid populates just enough env to get past the required-var
// guard in loadConfig.
func setMinimalValid(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("GRAFANA_URL", "http://grafana.local")
	t.Setenv("GRAFANA_SA_TOKEN", "token")
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	clearEnv(t)
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for missing required env vars")
	}
	for _, want := range []string{"GRAFANA_URL", "GRAFANA_SA_TOKEN or GRAFANA_BASIC_AUTH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-var error should list %q: %v", want, err)
		}
	}
}

func TestLoadConfig_RejectsBothSATokenAndBasicAuth(t *testing.T) {
	// SA tokens and basic auth are mutually exclusive — supplying both
	// hides which the Grafana client would pick and risks a credential
	// leak via the unintended path. Startup must fail loudly.
	setMinimalValid(t)
	t.Setenv("GRAFANA_BASIC_AUTH", "user:pass")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got: %v", err)
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

func TestResolveLogFormat(t *testing.T) {
	cases := []struct {
		name       string
		logFormat  string
		inCluster  bool
		wantFormat string
		wantErr    bool
	}{
		{"unset, out-of-cluster → text", "", false, logFormatText, false},
		{"unset, in-cluster → json", "", true, logFormatJSON, false},
		{"explicit json wins out-of-cluster", "json", false, logFormatJSON, false},
		{"explicit text wins in-cluster", "text", true, logFormatText, false},
		{"case-insensitive", "JSON", false, logFormatJSON, false},
		{"unknown value is a hard error", "logfmt", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LOG_FORMAT", c.logFormat)
			if c.inCluster {
				t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
			} else {
				t.Setenv("KUBERNETES_SERVICE_HOST", "")
			}
			got, err := resolveLogFormat()
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.wantFormat {
				t.Errorf("got %q, want %q", got, c.wantFormat)
			}
		})
	}
}

func TestEnvDuration(t *testing.T) {
	cases := []struct {
		name, env string
		def, want time.Duration
		wantErr   bool
	}{
		{"unset", "", 30 * time.Second, 30 * time.Second, false},
		{"zero disables", "0", 30 * time.Second, 0, false},
		{"zero-seconds disables", "0s", 30 * time.Second, 0, false},
		{"valid", "5m", 30 * time.Second, 5 * time.Minute, false},
		{"malformed is a hard error", "not-a-duration", 30 * time.Second, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			got, err := envDuration("X", c.def)
			if c.wantErr {
				if err == nil {
					t.Fatalf("envDuration(%q): want error, got %s", c.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("envDuration(%q): unexpected error: %v", c.env, err)
			}
			if got != c.want {
				t.Errorf("envDuration(%q, %s) = %s, want %s", c.env, c.def, got, c.want)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name, env string
		def, want int
		wantErr   bool
	}{
		{"unset", "", 128 * 1024, 128 * 1024, false},
		{"zero valid", "0", 128 * 1024, 0, false},
		{"valid", "1024", 128 * 1024, 1024, false},
		{"malformed is a hard error", "not-a-number", 128 * 1024, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			got, err := envInt("X", c.def)
			if c.wantErr {
				if err == nil {
					t.Fatalf("envInt(%q): want error, got %d", c.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("envInt(%q): unexpected error: %v", c.env, err)
			}
			if got != c.want {
				t.Errorf("envInt(%q, %d) = %d, want %d", c.env, c.def, got, c.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		name, env string
		def, want bool
		wantErr   bool
	}{
		{"unset default false", "", false, false, false},
		{"unset default true", "", true, true, false},
		{"true", "true", false, true, false},
		{"false", "false", true, false, false},
		{"1", "1", false, true, false},
		{"DEBUG=yes is a hard error", "yes", false, false, true},
		{"malformed is a hard error", "not-a-bool", true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("X", c.env)
			got, err := envBool("X", c.def)
			if c.wantErr {
				if err == nil {
					t.Fatalf("envBool(%q): want error, got %v", c.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("envBool(%q): unexpected error: %v", c.env, err)
			}
			if got != c.want {
				t.Errorf("envBool(%q, %v) = %v, want %v", c.env, c.def, got, c.want)
			}
		})
	}
}
