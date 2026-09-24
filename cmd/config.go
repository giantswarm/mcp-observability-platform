package cmd

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/mcp-toolkit/middleware/responsecap"
	"github.com/giantswarm/mcp-toolkit/middleware/timeout"
)

// Grafana auth modes accepted on GRAFANA_AUTH_MODE (case-insensitive).
const (
	grafanaAuthModeSAToken   = "serviceAccountToken"
	grafanaAuthModeBasicAuth = "basicAuth"
	grafanaAuthModeJWT       = "jwt"
)

// defaultGrafanaJWTHeader is the GRAFANA_JWT_HEADER default. It must
// match Grafana's [auth.jwt] header_name.
const defaultGrafanaJWTHeader = "X-JWT-Assertion"

type config struct {
	GrafanaURL       string
	GrafanaSAToken   string
	GrafanaBasicAuth string

	// GrafanaAuthMode is the resolved GRAFANA_AUTH_MODE, never empty
	// after loadConfig. Unset GRAFANA_AUTH_MODE infers serviceAccountToken
	// or basicAuth from which credential is set.
	GrafanaAuthMode string
	// GrafanaJWTHeader is the header carrying the caller's Dex ID token
	// to Grafana. Set only in jwt mode. Env: GRAFANA_JWT_HEADER.
	GrafanaJWTHeader string

	// ToolTimeout is the per-tool-call context deadline. Zero disables the
	// middleware; a malformed TOOL_TIMEOUT env value fails startup.
	ToolTimeout time.Duration
	// MaxResponseBytes caps tool response TextContent size. Zero disables
	// capping; a malformed TOOL_MAX_RESPONSE_BYTES env value fails startup.
	MaxResponseBytes int

	// Debug enables debug-level logging. Env: DEBUG; --debug flag overrides.
	Debug bool

	// LogFormat selects the slog handler ("json" or "text"). Defaults to
	// "json" when KUBERNETES_SERVICE_HOST is set, else "text". LOG_FORMAT
	// overrides.
	LogFormat string
}

func loadConfig() (*config, error) {
	debug, err := envBool("DEBUG", false)
	if err != nil {
		return nil, err
	}
	logFormat, err := resolveLogFormat()
	if err != nil {
		return nil, err
	}
	toolTimeout, err := envDuration("TOOL_TIMEOUT", timeout.DefaultTimeout)
	if err != nil {
		return nil, err
	}
	maxResponseBytes, err := envInt("TOOL_MAX_RESPONSE_BYTES", responsecap.DefaultLimit)
	if err != nil {
		return nil, err
	}
	c := &config{
		GrafanaURL:       os.Getenv("GRAFANA_URL"),
		GrafanaSAToken:   os.Getenv("GRAFANA_SA_TOKEN"),
		GrafanaBasicAuth: os.Getenv("GRAFANA_BASIC_AUTH"),
		ToolTimeout:      toolTimeout,
		MaxResponseBytes: maxResponseBytes,
		Debug:            debug,
		LogFormat:        logFormat,
	}
	var missing []string
	if c.GrafanaURL == "" {
		missing = append(missing, "GRAFANA_URL")
	}
	switch mode := os.Getenv("GRAFANA_AUTH_MODE"); strings.ToLower(mode) {
	case "":
		if c.GrafanaSAToken == "" && c.GrafanaBasicAuth == "" {
			missing = append(missing, "GRAFANA_SA_TOKEN or GRAFANA_BASIC_AUTH")
		}
		if c.GrafanaSAToken != "" && c.GrafanaBasicAuth != "" {
			return nil, fmt.Errorf("GRAFANA_SA_TOKEN and GRAFANA_BASIC_AUTH are mutually exclusive — set one and unset the other")
		}
		c.GrafanaAuthMode = grafanaAuthModeSAToken
		if c.GrafanaBasicAuth != "" {
			c.GrafanaAuthMode = grafanaAuthModeBasicAuth
		}
	case strings.ToLower(grafanaAuthModeSAToken):
		if c.GrafanaBasicAuth != "" {
			return nil, fmt.Errorf("GRAFANA_AUTH_MODE=%s: unset GRAFANA_BASIC_AUTH", grafanaAuthModeSAToken)
		}
		if c.GrafanaSAToken == "" {
			missing = append(missing, "GRAFANA_SA_TOKEN")
		}
		c.GrafanaAuthMode = grafanaAuthModeSAToken
	case strings.ToLower(grafanaAuthModeBasicAuth):
		if c.GrafanaSAToken != "" {
			return nil, fmt.Errorf("GRAFANA_AUTH_MODE=%s: unset GRAFANA_SA_TOKEN", grafanaAuthModeBasicAuth)
		}
		if c.GrafanaBasicAuth == "" {
			missing = append(missing, "GRAFANA_BASIC_AUTH")
		}
		c.GrafanaAuthMode = grafanaAuthModeBasicAuth
	case strings.ToLower(grafanaAuthModeJWT):
		// Each caller's own token authenticates; a shared credential
		// would be ignored, so refuse it rather than leave it unused.
		if c.GrafanaSAToken != "" || c.GrafanaBasicAuth != "" {
			return nil, fmt.Errorf("GRAFANA_AUTH_MODE=%s: unset GRAFANA_SA_TOKEN and GRAFANA_BASIC_AUTH", grafanaAuthModeJWT)
		}
		c.GrafanaAuthMode = grafanaAuthModeJWT
		c.GrafanaJWTHeader = envOr("GRAFANA_JWT_HEADER", defaultGrafanaJWTHeader)
	default:
		return nil, fmt.Errorf("GRAFANA_AUTH_MODE=%q: want %q, %q or %q", mode, grafanaAuthModeSAToken, grafanaAuthModeBasicAuth, grafanaAuthModeJWT)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

const (
	logFormatJSON = "json"
	logFormatText = "text"
)

// resolveLogFormat picks the slog handler based on LOG_FORMAT, or infers one
// from KUBERNETES_SERVICE_HOST when LOG_FORMAT is unset. An unknown value is
// a hard error so a typo doesn't silently fall back to text on a JSON-parsed
// log pipeline.
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

// envDuration reads a duration env var. "0"/"0s" disables; malformed
// fails startup rather than falling back to def.
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

// envInt reads an int env var. A malformed value fails startup.
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

// envBool reads a bool env var. Only strconv.ParseBool forms are accepted;
// a typo like `DEBUG=yes` fails startup instead of silently becoming false.
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
