package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// stubOrgLister returns no orgs; the Tempo binder skips registration
// when the registry is empty, which is what these construction tests
// want.
type stubOrgLister struct{}

func (stubOrgLister) List(context.Context) ([]authz.Organization, error) { return nil, nil }

// stubGrafana satisfies grafana.Client for the non-nil check in New;
// methods return zero values and are not invoked.
type stubGrafana struct {
	grafana.Client
}

func (stubGrafana) VerifyServerAdmin(context.Context) error { return nil }
func (stubGrafana) LookupUser(context.Context, string) (*grafana.User, error) {
	return nil, nil
}
func (stubGrafana) UserOrgs(context.Context, int64) ([]grafana.UserOrgMembership, error) {
	return nil, nil
}
func (stubGrafana) DatasourceProxy(context.Context, grafana.RequestOpts, int64, string, url.Values) (json.RawMessage, error) {
	return nil, nil
}

// goodCfg is a Config every test starts from, then zeroes one field
// at a time to assert validation. URL/cred validation lives in
// tools.RegisterAll, surfaced through server.New's wrapped error.
func goodCfg() Config {
	return Config{
		Logger:        slog.Default(),
		Authorizer:    &authztest.Fake{},
		OrgLister:     stubOrgLister{},
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
		{"no orglister", func(c *Config) { c.OrgLister = nil }, "OrgLister is required"},
		{"no grafana", func(c *Config) { c.Grafana = nil }, "Grafana is required"},
		{"no url", func(c *Config) { c.GrafanaURL = "" }, "grafana URL is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := goodCfg()
			c.mutate(&cfg)
			_, err := New(context.Background(), cfg)
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
	if _, err := New(context.Background(), goodCfg()); err != nil {
		t.Fatalf("New with empty Version: %v", err)
	}
}
