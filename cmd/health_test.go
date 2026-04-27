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
// panics — exactly what we want if setupHealth ever grows a new upstream
// dependency we haven't accounted for here. Only Ping is overridden.
type stubGrafanaClient struct {
	grafana.Client
	pingErr error
}

func (s stubGrafanaClient) Ping(_ context.Context) error { return s.pingErr }

// dexStub returns an httptest.Server that serves a minimal valid
// /.well-known/openid-configuration response for the dex probe.
func dexStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"x","authorization_endpoint":"x","jwks_uri":"x"}`))
	}))
}

// runHealth wires setupHealth and returns both the readyz status code and
// a flat string of per-probe statuses + error messages so tests can assert
// on which probe failed without parsing JSON over HTTP.
func runHealth(t *testing.T, gf grafana.Client, orgs orgLister, alive *atomic.Bool) (readyzCode int, probesSummary string) {
	t.Helper()
	dex := dexStub(t)
	defer dex.Close()
	h := setupHealth("test", dex.URL, gf, orgs, alive)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	readyz := httptest.NewRecorder()
	mux.ServeHTTP(readyz, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	var b strings.Builder
	for name, c := range h.Snapshot(context.Background()) {
		_, _ = b.WriteString(name)
		_, _ = b.WriteString("=")
		_, _ = b.WriteString(c.Status)
		if c.Message != "" {
			_, _ = b.WriteString(":")
			_, _ = b.WriteString(c.Message)
		}
		_, _ = b.WriteString(" ")
	}
	return readyz.Code, b.String()
}

func TestSetupHealth_AllProbesOK(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	listOrgs := func(context.Context) (int, error) { return 3, nil }

	code, _ := runHealth(t, stubGrafanaClient{}, listOrgs, &alive)
	if code != http.StatusOK {
		t.Errorf("readyz = %d, want 200 when all probes pass", code)
	}
}

func TestSetupHealth_GrafanaPingFailureSurfaces(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	listOrgs := func(context.Context) (int, error) { return 0, nil }

	code, body := runHealth(t,
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

	code, body := runHealth(t, stubGrafanaClient{}, listOrgs, &alive)
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

	code, body := runHealth(t, stubGrafanaClient{}, listOrgs, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite list failure; body: %s", body)
	}
	if !strings.Contains(body, "throttled") {
		t.Errorf("upstream list error should flow through to probe body: %s", body)
	}
}
