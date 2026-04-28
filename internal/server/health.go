// Package server — health.go: liveness + readiness HTTP handlers.
//
// /healthz is unconditional 200 (liveness — process responsive). /readyz
// runs every registered probe serially under a single per-request
// deadline; first probe to error returns 503. Probes SHOULD honour ctx
// — if one ignores it and runs longer than the deadline, the deadline
// fires and a probe goroutine may leak until the probe eventually
// returns. HTTPProbe honours ctx; cmd-side probes (Grafana ping,
// cache-alive, list-orgs) all honour ctx.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Probe runs as part of the readiness check. nil err = pass.
type Probe func(ctx context.Context) error

// Health serves /healthz and /readyz. Probes are evaluated serially in
// registration order; the first error wins.
type Health struct {
	timeout time.Duration

	mu     sync.RWMutex
	probes []namedProbe
}

type namedProbe struct {
	name string
	fn   Probe
}

// NewHealth builds an empty checker. timeout is the per-request
// deadline applied to all probes for one /readyz call.
func NewHealth(timeout time.Duration) *Health {
	return &Health{timeout: timeout}
}

// Register appends a probe under name. Order matters: probes evaluate
// serially in registration order, so put the cheapest first.
func (h *Health) Register(name string, fn Probe) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.probes = append(h.probes, namedProbe{name: name, fn: fn})
}

// Mount attaches /healthz and /readyz to mux.
func (h *Health) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.Liveness)
	mux.HandleFunc("/readyz", h.Readiness)
}

// Liveness is unconditionally 200 — flaky downstreams must not restart
// the pod.
func (h *Health) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Readiness runs every probe and returns 200 only if all pass. Body
// names the failing probe + its error so kubelet logs are actionable.
func (h *Health) Readiness(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	probes := make([]namedProbe, len(h.probes))
	copy(probes, h.probes)
	h.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	for _, p := range probes {
		if err := p.fn(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "probe %q failed: %v\n", p.name, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// HTTPProbe returns a Probe that GETs url and errors on non-2xx.
// nil client gets a default 2-second-timeout client.
func HTTPProbe(client *http.Client, url string) Probe {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return func(ctx context.Context) error {
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
}
