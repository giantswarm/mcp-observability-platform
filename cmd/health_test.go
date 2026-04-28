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
)

// stubLister implements authz.OrgLister with a fixed result. Returning
// nil/no error simulates a healthy in-memory cache; an err simulates an
// apiserver failure.
type stubLister struct {
	orgs []authz.Organization
	err  error
}

func (s stubLister) List(_ context.Context) ([]authz.Organization, error) {
	return s.orgs, s.err
}

func runReadyz(t *testing.T, lister authz.OrgLister, alive *atomic.Bool) (code int, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	buildObsMux(lister, alive).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code, rec.Body.String()
}

func TestReadyz_PassesWhenCacheAliveAndListSucceeds(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)

	code, _ := runReadyz(t, stubLister{}, &alive)
	if code != http.StatusOK {
		t.Errorf("readyz = %d, want 200", code)
	}
}

func TestReadyz_FailsWhenCacheStopped(t *testing.T) {
	var alive atomic.Bool
	alive.Store(false)
	// stubLister would "succeed" on stale data — the cacheAlive gate must
	// still fail the probe.
	code, body := runReadyz(t, stubLister{}, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite dead cache; body: %s", body)
	}
	if !strings.Contains(body, "cache stopped") {
		t.Errorf("dead-cache body should carry 'cache stopped': %s", body)
	}
}

func TestReadyz_FailsWhenListErrors(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	lister := stubLister{err: errors.New("apiserver throttled")}

	code, _ := runReadyz(t, lister, &alive)
	if code == http.StatusOK {
		t.Errorf("readyz = 200 despite list failure")
	}
}
