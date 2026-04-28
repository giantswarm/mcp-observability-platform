package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// stubGrafanaClient embeds grafana.Client (nil) so any unstubbed method
// panics — flags any new upstream dependency mountHealth grows that the
// tests forgot to cover. Only Ping is overridden.
type stubGrafanaClient struct {
	grafana.Client
	pingErr error
}

func (s stubGrafanaClient) Ping(_ context.Context) error { return s.pingErr }

// dexStub serves a minimal valid /.well-known/openid-configuration so the
// Dex probe sees a 2xx.
func dexStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"x","authorization_endpoint":"x","jwks_uri":"x"}`))
	}))
}

// runReadyz mounts mountHealth on a fresh mux and exercises /readyz,
// returning the status code and response body. Body carries the failing
// probe name + error when readyz returns 503.
func runReadyz(t *testing.T, gf grafana.Client, orgs orgLister, alive *atomic.Bool) (code int, body string) {
	t.Helper()
	dex := dexStub(t)
	defer dex.Close()
	mux := http.NewServeMux()
	mountHealth(mux, dex.URL, gf, orgs, alive)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code, rec.Body.String()
}

func TestSetupHealth_AllProbesOK(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	listOrgs := func(context.Context) (int, error) { return 3, nil }

	code, _ := runReadyz(t, stubGrafanaClient{}, listOrgs, &alive)
	if code != http.StatusOK {
		t.Errorf("readyz = %d, want 200 when all probes pass", code)
	}
}

func TestSetupHealth_GrafanaPingFailureSurfaces(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	listOrgs := func(context.Context) (int, error) { return 0, nil }

	code, body := runReadyz(t,
		stubGrafanaClient{pingErr: errors.New("grafana down")},
		listOrgs,
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

	// listOrgs would "succeed" on stale data — the cacheAlive gate must
	// still fail the probe.
	listOrgs := func(context.Context) (int, error) { return 0, nil }

	code, body := runReadyz(t, stubGrafanaClient{}, listOrgs, &alive)
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
	listOrgs := func(context.Context) (int, error) {
		return 0, errors.New("apiserver throttled")
	}

	code, body := runReadyz(t, stubGrafanaClient{}, listOrgs, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite list failure; body: %s", body)
	}
	if !strings.Contains(body, "throttled") {
		t.Errorf("upstream list error should flow through to probe body: %s", body)
	}
}
