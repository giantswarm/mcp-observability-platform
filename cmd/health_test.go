package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// stubGrafanaClient embeds grafana.Client (nil) so any unstubbed method
// panics — exactly what we want if setupHealth ever grows a new upstream
// dependency we haven't accounted for here. Only Ping is overridden.
type stubGrafanaClient struct {
	grafana.Client
	pingErr error
}

func (s stubGrafanaClient) Ping(_ context.Context) error { return s.pingErr }

// stubLister implements authz.OrgLister with a fixed result for the
// readiness probe. Returning nil/no error simulates a healthy
// in-memory cache; an err simulates an apiserver failure.
type stubLister struct {
	orgs []authz.Organization
	err  error
}

func (s stubLister) List(_ context.Context) ([]authz.Organization, error) {
	return s.orgs, s.err
}

// dexStub serves a minimal valid /.well-known/openid-configuration so the
// Dex probe sees a 2xx.
func dexStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"x","authorization_endpoint":"x","jwks_uri":"x"}`))
	}))
}

// runReadyz wires setupHealth and exercises /readyz, returning the
// status code and response body. Body carries the failing probe name
// + error when readyz returns 503 (see Health.Readiness).
func runReadyz(t *testing.T, gf grafana.Client, lister authz.OrgLister, alive *atomic.Bool) (code int, body string) {
	t.Helper()
	dex := dexStub(t)
	defer dex.Close()
	h := setupHealth(dex.URL, gf, lister, alive)
	mux := http.NewServeMux()
	h.Mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code, rec.Body.String()
}

func TestSetupHealth_AllProbesOK(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)

	code, _ := runReadyz(t, stubGrafanaClient{}, stubLister{}, &alive)
	if code != http.StatusOK {
		t.Errorf("readyz = %d, want 200 when all probes pass", code)
	}
}

func TestSetupHealth_GrafanaPingFailureSurfaces(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)

	code, body := runReadyz(t,
		stubGrafanaClient{pingErr: errors.New("grafana down")},
		stubLister{},
		&alive,
	)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite grafana down; body: %s", body)
	}
	if !strings.Contains(body, "grafana") {
		t.Errorf("failing readyz body should name the failing probe: %s", body)
	}
}

func TestSetupHealth_DeadCacheSurfaces(t *testing.T) {
	var alive atomic.Bool
	alive.Store(false) // informer Start has exited

	// stubLister would "succeed" on stale data — the cacheAlive gate must
	// still fail the probe.
	code, body := runReadyz(t, stubGrafanaClient{}, stubLister{}, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite dead cache; body: %s", body)
	}
	if !strings.Contains(body, "cache stopped") {
		t.Errorf("dead-cache body should carry 'cache stopped': %s", body)
	}
}

func TestSetupHealth_ListOrgsErrorSurfaces(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	lister := stubLister{err: errors.New("apiserver throttled")}

	code, body := runReadyz(t, stubGrafanaClient{}, lister, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite list failure; body: %s", body)
	}
	if !strings.Contains(body, "throttled") {
		t.Errorf("upstream list error should flow through to probe body: %s", body)
	}
}
