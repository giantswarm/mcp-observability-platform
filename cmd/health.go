package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// readinessTimeout is the deadline applied across all probes per /readyz call.
// Short on purpose — readyz must not hang the kubelet.
const readinessTimeout = 2 * time.Second

// orgLister returns the count of GrafanaOrganization CRs currently visible
// through the informer cache. A function type rather than a full
// ctrlcache.Cache dependency keeps mountHealth testable without a K8s stub.
type orgLister func(ctx context.Context) (int, error)

// mountHealth attaches /healthz (unconditional 200 — flaky downstreams
// must not restart the pod) and /readyz (the three production probes:
// Grafana reachability, Dex OIDC discovery, K8s informer-cache liveness).
//
// cacheAlive is flipped to false by the caller when the informer's Start
// goroutine exits on a non-canceled error — without it, the cache's List
// keeps returning the last-known snapshot and readyz lies.
func mountHealth(
	mux *http.ServeMux,
	dexIssuerURL string,
	gfClient grafana.Client,
	listOrgs orgLister,
	cacheAlive *atomic.Bool,
) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	dexURL := strings.TrimRight(dexIssuerURL, "/") + "/.well-known/openid-configuration"
	dexClient := &http.Client{Timeout: readinessTimeout}

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := gfClient.Ping(ctx); err != nil {
			fail(w, "grafana", err)
			return
		}
		if err := httpGetOK(ctx, dexClient, dexURL); err != nil {
			fail(w, "dex", err)
			return
		}
		if !cacheAlive.Load() {
			fail(w, "k8s_cache", errors.New("controller-runtime cache stopped"))
			return
		}
		if _, err := listOrgs(ctx); err != nil {
			fail(w, "k8s_cache", err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func fail(w http.ResponseWriter, probe string, err error) {
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, "probe %q failed: %v\n", probe, err)
}

func httpGetOK(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}
