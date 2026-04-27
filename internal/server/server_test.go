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
)

// stubResolver is an authz.Authorizer implementation that's enough to pass
// server.New's non-nil check. Methods are never actually invoked in these
// validation-path tests.
type stubResolver struct{}

func (stubResolver) RequireOrg(context.Context, string, authz.Role) (authz.Organization, error) {
	return authz.Organization{}, nil
}
func (stubResolver) ListOrgs(context.Context) (map[string]authz.Organization, error) {
	return nil, nil
}

// stubGrafana satisfies grafana.Client for server.New's non-nil check. All
// methods return zero values; they're never invoked in these tests.
type stubGrafana struct{}

func (stubGrafana) Ping(context.Context) error              { return nil }
func (stubGrafana) VerifyServerAdmin(context.Context) error { return nil }
func (stubGrafana) BaseURL() *url.URL                       { return nil }
func (stubGrafana) HasImageRenderer(context.Context) (bool, error) {
	return false, nil
}
func (stubGrafana) LookupUser(context.Context, string) (*struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Login string `json:"login"`
}, error) {
	return nil, nil
}
func (stubGrafana) UserOrgs(context.Context, int64) ([]grafana.UserOrgMembership, error) {
	return nil, nil
}
func (stubGrafana) GetDashboard(context.Context, grafana.RequestOpts, string) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) SearchDashboards(context.Context, grafana.RequestOpts, string, int) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) SearchFolders(context.Context, grafana.RequestOpts, string, int) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) ListDatasources(context.Context, grafana.RequestOpts) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) GetDatasource(context.Context, grafana.RequestOpts, string) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) GetAnnotations(context.Context, grafana.RequestOpts, url.Values) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) GetAnnotationTags(context.Context, grafana.RequestOpts, url.Values) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) DatasourceProxy(context.Context, grafana.RequestOpts, int64, string, url.Values) (json.RawMessage, error) {
	return nil, nil
}
func (stubGrafana) RenderPanel(context.Context, grafana.RequestOpts, string, int, url.Values) ([]byte, string, error) {
	return nil, "", nil
}

func TestNew_RejectsMissingDependencies(t *testing.T) {
	// dummy non-nil values for the fields we're NOT testing in each case
	log := slog.Default()
	var resolver authz.Authorizer = stubResolver{}
	gf := stubGrafana{} // ditto

	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no logger", Config{Authorizer: resolver, Grafana: gf}, "Logger is required"},
		{"no authorizer", Config{Logger: log, Grafana: gf}, "Authorizer is required"},
		{"no grafana", Config{Logger: log, Authorizer: resolver}, "Grafana is required"},
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
	// When Version is empty, New should still succeed and default to "dev".
	// We can't easily inspect the mcp-go server's stored version, but we can
	// at least confirm construction doesn't fail on the default path.
	_, err := New(Config{
		Logger:     slog.Default(),
		Authorizer: stubResolver{},
		Grafana:    stubGrafana{},
	})
	if err != nil {
		t.Fatalf("New with empty Version: %v", err)
	}
}
