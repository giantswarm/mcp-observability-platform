package cmd

import (
	"errors"
	"net/http"
	"sync/atomic"

	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// readyzChecker probes only pod-local state. Shared downstreams
// (Grafana, Dex) are excluded on purpose: a flap there would fail
// /readyz on every replica at once and remove the Service's last
// endpoint. cacheAlive guards against the informer goroutine having
// exited — without it, List would silently return stale snapshots.
func readyzChecker(lister authz.OrgLister, cacheAlive *atomic.Bool) healthz.Checker {
	return func(req *http.Request) error {
		if !cacheAlive.Load() {
			return errors.New("controller-runtime cache stopped")
		}
		_, err := lister.List(req.Context())
		return err
	}
}
