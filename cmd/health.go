package cmd

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
)

// orgLister returns the count of GrafanaOrganization CRs currently visible
// through the informer cache, or an error if the list call fails. A small
// function type rather than a full ctrlcache.Cache dependency keeps
// setupHealth testable without a K8s stub.
type orgLister func(ctx context.Context) (int, error)

// setupHealth wires the production readiness probes against a fresh
// HealthChecker: Grafana reachability (Ping), Dex OIDC discovery, and K8s
// informer-cache liveness. cacheAlive is flipped to false by the caller
// when the informer's Start goroutine exits on a non-canceled error —
// without it, the cache's List keeps returning the last-known snapshot
// and readyz lies.
//
// 2s per-check deadline keeps kubelet probes honest.
func setupHealth(
	version string,
	dexIssuerURL string,
	gfClient grafana.Client,
	listOrgs orgLister,
	cacheAlive *atomic.Bool,
) *server.HealthChecker {
	health := server.NewHealthChecker(version, 2*time.Second)
	health.Register("grafana", func(ctx context.Context) (any, error) {
		return nil, gfClient.Ping(ctx)
	})
	health.Register("dex", server.HTTPProbe(nil, strings.TrimRight(dexIssuerURL, "/")+"/.well-known/openid-configuration"))
	health.Register("k8s_cache", func(ctx context.Context) (any, error) {
		if !cacheAlive.Load() {
			return nil, errors.New("controller-runtime cache stopped")
		}
		n, err := listOrgs(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]int{"orgs": n}, nil
	})
	return health
}
