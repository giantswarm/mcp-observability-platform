package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

// stubBridge is a minimal *upstream.Bridge that's enough for server.New's
// non-nil check. The bridge is never actually exercised by these tests —
// they don't invoke tool handlers.
func stubBridge(az authz.Authorizer) *upstream.Bridge {
	br, err := upstream.NewBridge(az, stubGrafana{}, "http://grafana.local", "stub", nil)
	if err != nil {
		panic(err)
	}
	return br
}

// stubResolver is an authz.Authorizer implementation that's enough to pass
// server.New's non-nil check. Methods are never invoked in these tests.
type stubResolver struct{}

func (stubResolver) RequireOrg(context.Context, string, authz.Role) (authz.Organization, error) {
	return authz.Organization{}, nil
}
func (stubResolver) ListOrgs(context.Context) (map[string]authz.Organization, error) {
	return nil, nil
}

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

func TestNew_RejectsMissingDependencies(t *testing.T) {
	log := slog.Default()
	var resolver authz.Authorizer = stubResolver{}
	gf := stubGrafana{}

	br := stubBridge(resolver)
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no logger", Config{Authorizer: resolver, Grafana: gf, Bridge: br}, "Logger is required"},
		{"no authorizer", Config{Logger: log, Grafana: gf, Bridge: br}, "Authorizer is required"},
		{"no grafana", Config{Logger: log, Authorizer: resolver, Bridge: br}, "Grafana is required"},
		{"no bridge", Config{Logger: log, Authorizer: resolver, Grafana: gf}, "Bridge is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.cfg)
			if err == nil {
				t.Fatalf("New(%+v) = nil error, want %q", c.cfg, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestNew_DefaultsVersion(t *testing.T) {
	// Empty Version must still construct (defaults to "dev").
	az := stubResolver{}
	_, err := New(Config{
		Logger:     slog.Default(),
		Authorizer: az,
		Grafana:    stubGrafana{},
		Bridge:     stubBridge(az),
	})
	if err != nil {
		t.Fatalf("New with empty Version: %v", err)
	}
}
