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

// setupHealth wires the production readiness probes: Grafana
// reachability (Ping), Dex OIDC discovery, and K8s informer-cache
// liveness. cacheAlive is flipped to false by the caller when the
// informer's Start goroutine exits on a non-canceled error — without
// it, the cache's List keeps returning the last-known snapshot and
// readyz lies.
//
// 2s deadline applied across all probes per readiness call.
func setupHealth(
	dexIssuerURL string,
	gfClient grafana.Client,
	listOrgs orgLister,
	cacheAlive *atomic.Bool,
) *server.Health {
	health := server.NewHealth(2 * time.Second)
	health.Register("grafana", func(ctx context.Context) error {
		return gfClient.Ping(ctx)
	})
	health.Register("dex", server.HTTPProbe(nil, strings.TrimRight(dexIssuerURL, "/")+"/.well-known/openid-configuration"))
	health.Register("k8s_cache", func(ctx context.Context) error {
		if !cacheAlive.Load() {
			return errors.New("controller-runtime cache stopped")
		}
		_, err := listOrgs(ctx)
		return err
	})
	return health
}
