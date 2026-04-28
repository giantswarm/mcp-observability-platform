package cmd

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/server/middleware"
)

// config is the non-OAuth slice of process configuration. OAuth, Dex, valkey,
// and encryption-key handling all flow through mcp-oauth/oauthconfig (see
// oauth.go) — keeping that surface there avoids drift between this binary
// and the upstream contract documented in the helm chart.
type config struct {
	GrafanaURL       string
	GrafanaSAToken   string
	GrafanaBasicAuth string

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

// loadConfig reads the non-OAuth env vars, validates them, and returns a
// populated *config. Fails fast on missing required vars or unparseable values.
func loadConfig() (*config, error) {
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
	if c.GrafanaSAToken == "" && c.GrafanaBasicAuth == "" {
		missing = append(missing, "GRAFANA_SA_TOKEN or GRAFANA_BASIC_AUTH")
	}
	if c.GrafanaSAToken != "" && c.GrafanaBasicAuth != "" {
		return nil, fmt.Errorf("GRAFANA_SA_TOKEN and GRAFANA_BASIC_AUTH are mutually exclusive — set one and unset the other")
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

// LogFormat values accepted by newLogger.
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

// envDuration reads a time.Duration env var. "0"/"0s" maps to 0 (the
// conventional "disable this feature" marker); a malformed value fails
// startup rather than silently using the default.
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
