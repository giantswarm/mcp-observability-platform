package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// stubGrafana satisfies grafana.Client for server.New's non-nil check.
// All methods return zero values; they're never invoked in these tests.
type stubGrafana struct {
	grafana.Client // embedded — keeps the stub honest if the interface grows
}

func (stubGrafana) Ping(context.Context) error              { return nil }
func (stubGrafana) VerifyServerAdmin(context.Context) error { return nil }
func (stubGrafana) LookupUser(context.Context, string) (*grafana.User, error) {
	return nil, nil
}
func (stubGrafana) LookupDatasourceUIDByID(context.Context, grafana.RequestOpts, int64) (string, error) {
	return "", nil
}
func (stubGrafana) UserOrgs(context.Context, int64) ([]grafana.UserOrgMembership, error) {
	return nil, nil
}
func (stubGrafana) DatasourceProxy(context.Context, grafana.RequestOpts, int64, string, url.Values) (json.RawMessage, error) {
	return nil, nil
}

// goodCfg is a Config with every required field populated; tests start
// from this and zero one field at a time to assert the validation.
func goodCfg() Config {
	return Config{
		Logger:        slog.Default(),
		Authorizer:    &authztest.Fake{},
		Grafana:       stubGrafana{},
		GrafanaURL:    "http://grafana.local",
		GrafanaAPIKey: "stub",
	}
}

func TestNew_RejectsMissingDependencies(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"no logger", func(c *Config) { c.Logger = nil }, "Logger is required"},
		{"no authorizer", func(c *Config) { c.Authorizer = nil }, "Authorizer is required"},
		{"no grafana", func(c *Config) { c.Grafana = nil }, "Grafana is required"},
		{"no url", func(c *Config) { c.GrafanaURL = "" }, "Grafana URL is required"},
		{"no creds", func(c *Config) { c.GrafanaAPIKey = "" }, "exactly one of APIKey or BasicAuth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := goodCfg()
			c.mutate(&cfg)
			_, err := New(cfg)
			if err == nil {
				t.Fatalf("New(%+v) = nil error, want %q", cfg, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestNew_DefaultsVersion(t *testing.T) {
	// Empty Version must still construct (defaults to "dev").
	if _, err := New(goodCfg()); err != nil {
		t.Fatalf("New with empty Version: %v", err)
	}
}
