package cmd

import (
	"errors"
	"net/http"
	"sync/atomic"

	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// readyzChecker probes only pod-local state and ensures the pods can list grafana organization CRs.
func readyzChecker(lister authz.OrgLister, cacheAlive *atomic.Bool) healthz.Checker {
	return func(req *http.Request) error {
		if !cacheAlive.Load() {
			return errors.New("controller-runtime cache stopped")
		}
		_, err := lister.List(req.Context())
		return err
	}
}
