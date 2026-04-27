package server

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

const (
	statusOK     = "ok"
	statusFailed = "failed"
)

type Check struct {
	Status     string
	DurationMs int64
	Message    string
	Extra      any
}

// CheckFn must honour ctx deadlines so probe latency stays bounded.
type CheckFn func(ctx context.Context) (extra any, err error)

// HealthChecker serves /healthz (liveness, always 200) and /readyz (503 if
// any registered check fails). Probes run concurrently with a shared
// deadline.
type HealthChecker struct {
	startTime time.Time
	version   string
	timeout   time.Duration

	mu     sync.RWMutex
	checks map[string]CheckFn
}

// NewHealthChecker builds a checker with no probes wired yet. timeout is
// the per-check deadline.
func NewHealthChecker(version string, timeout time.Duration) *HealthChecker {
	return &HealthChecker{
		startTime: time.Now(),
		version:   version,
		timeout:   timeout,
		checks:    map[string]CheckFn{},
	}
}

// Register adds a named probe; overwrites any existing probe with the same name.
func (h *HealthChecker) Register(name string, fn CheckFn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = fn
}

func (h *HealthChecker) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.Liveness)
	mux.HandleFunc("/readyz", h.Readiness)
}

// Liveness does NOT run readiness probes — a flaky downstream should not
// restart the pod.
func (h *HealthChecker) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *HealthChecker) Readiness(w http.ResponseWriter, r *http.Request) {
	results := h.Snapshot(r.Context())
	for _, c := range results {
		if c.Status != statusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// Snapshot runs every registered probe under the per-check timeout and
// returns the per-probe results. Used by Readiness for the 503 decision
// and by tests to assert on probe wiring without going through HTTP.
//
// A probe that ignores ctx and runs longer than the timeout has its
// "deadline exceeded" entry recorded and Snapshot returns; the
// goroutine running that probe leaks until the probe eventually
// completes. Documented as the trade-off vs blocking the readiness
// endpoint indefinitely on a misbehaving probe — probes SHOULD honour
// ctx (HTTPProbe does).
func (h *HealthChecker) Snapshot(parent context.Context) map[string]Check {
	h.mu.RLock()
	checks := make(map[string]CheckFn, len(h.checks))
	maps.Copy(checks, h.checks)
	h.mu.RUnlock()

	ctx, cancel := context.WithTimeout(parent, h.timeout)
	defer cancel()

	results := make(map[string]Check, len(checks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, fn := range checks {
		wg.Add(1)
		go func(name string, fn CheckFn) {
			defer wg.Done()
			start := time.Now()
			extra, err := fn(ctx)
			c := Check{
				DurationMs: time.Since(start).Milliseconds(),
				Extra:      extra,
			}
			if err == nil {
				c.Status = statusOK
			} else {
				c.Status = statusFailed
				c.Message = err.Error()
			}
			mu.Lock()
			results[name] = c
			mu.Unlock()
		}(name, fn)
	}

	// Wait for all probes OR the timeout — whichever comes first.
	// Late-finishing probes still acquire the lock and write into
	// `results`, but Snapshot returns a fresh `out` map so a late
	// write doesn't race with the caller's read.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	out := make(map[string]Check, len(checks))
	mu.Lock()
	for name := range checks {
		if c, ok := results[name]; ok {
			out[name] = c
		} else {
			out[name] = Check{Status: statusFailed, Message: "deadline exceeded", DurationMs: h.timeout.Milliseconds()}
		}
	}
	mu.Unlock()
	return out
}

// HTTPProbe returns a CheckFn that GETs url and reports ok on any 2xx.
func HTTPProbe(client *http.Client, url string) CheckFn {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return func(ctx context.Context) (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		// 2xx only. Guard against 1xx (e.g. 101 Switching Protocols from a
		// misconfigured handler) which resp.StatusCode exposes even though
		// client.Do usually consumes them.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil, nil
	}
}
