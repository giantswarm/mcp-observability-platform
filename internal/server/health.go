package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

// Check status values reported under Check.Status and the top-level "status"
// field of the detailed response.
const (
	statusOK     = "ok"
	statusFailed = "failed"
)

// Check is one probe result inside a detailed health response.
type Check struct {
	Status     string `json:"status"` // statusOK | statusFailed
	DurationMs int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Extra      any    `json:"extra,omitempty"` // probe-specific payload (e.g. cache size)
}

// CheckFn is a single named readiness probe. It must honour ctx deadlines
// so probe latency stays bounded even when downstream is hung.
type CheckFn func(ctx context.Context) (extra any, err error)

// HealthChecker serves three endpoints on the observability mux:
//   - /healthz          — liveness, always 200 unless the process itself is dead.
//   - /readyz           — readiness, 503 when any registered check fails.
//   - /healthz/detailed — JSON summary with per-check status + duration + uptime.
//
// Probes run concurrently with a shared deadline so /readyz stays fast even
// when several checks are slow.
type HealthChecker struct {
	startTime time.Time
	version   string
	timeout   time.Duration

	mu     sync.RWMutex
	checks map[string]CheckFn
}

// NewHealthChecker returns a checker with no probes wired yet. Use Register
// to add probes and RegisterHandlers to mount the HTTP endpoints.
//
// timeout is the per-check deadline; checks that exceed it are reported as
// failed with "deadline exceeded".
func NewHealthChecker(version string, timeout time.Duration) *HealthChecker {
	return &HealthChecker{
		startTime: time.Now(),
		version:   version,
		timeout:   timeout,
		checks:    map[string]CheckFn{},
	}
}

// Register adds a named probe. Overwrites any previous probe with the same
// name (handy for tests).
func (h *HealthChecker) Register(name string, fn CheckFn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = fn
}

// RegisterHandlers mounts the three endpoints onto mux.
func (h *HealthChecker) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.Liveness)
	mux.HandleFunc("/readyz", h.Readiness)
	mux.HandleFunc("/healthz/detailed", h.Detailed)
}

// Liveness answers the kubelet: "are we still running?" Returns 200 as long
// as the process can serve HTTP. Does NOT run readiness probes — that would
// make a flaky downstream restart the pod, which rarely helps.
func (h *HealthChecker) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Readiness runs all probes concurrently and returns 200 on full pass,
// 503 if any probe failed. Body is empty — use /healthz/detailed for
// human-readable output.
func (h *HealthChecker) Readiness(w http.ResponseWriter, r *http.Request) {
	results := h.runAll(r.Context())
	for _, c := range results {
		if c.Status != statusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// Detailed runs all probes concurrently and returns a JSON body with per-
// check status + duration, overall status, uptime, version. Content-type is
// application/json; HTTP status matches Readiness (200 on pass, 503 on any
// fail).
func (h *HealthChecker) Detailed(w http.ResponseWriter, r *http.Request) {
	results := h.runAll(r.Context())
	overall := statusOK
	for _, c := range results {
		if c.Status != statusOK {
			overall = statusFailed
			break
		}
	}
	body := struct {
		Status        string           `json:"status"`
		UptimeSeconds float64          `json:"uptime_seconds"`
		Version       string           `json:"version"`
		Checks        map[string]Check `json:"checks"`
	}{
		Status:        overall,
		UptimeSeconds: time.Since(h.startTime).Seconds(),
		Version:       h.version,
		Checks:        results,
	}
	// Marshal to a buffer before writing so a MarshalJSON error from any
	// Check.Extra value surfaces as a 500 instead of a truncated 200 body.
	buf, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "failed to marshal health response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if overall != statusOK {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.Write(buf)
}

// runAll executes every registered probe in parallel with h.timeout. Probes
// that exceed the deadline report "deadline exceeded".
func (h *HealthChecker) runAll(parent context.Context) map[string]Check {
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
	wg.Wait()

	// Fold any check that didn't complete on time into a deadline-exceeded
	// result (belt-and-braces — runAll blocks on wg.Wait anyway, but a probe
	// that ignores ctx could still slip through; this guarantees every
	// registered name has an entry).
	for name := range checks {
		if _, ok := results[name]; !ok {
			results[name] = Check{Status: statusFailed, Message: "deadline exceeded", DurationMs: h.timeout.Milliseconds()}
		}
	}
	return results
}

// HTTPProbe returns a CheckFn that GETs url and reports ok on any 2xx.
// Useful for Dex's /.well-known/openid-configuration and similar endpoints
// that have no dedicated client in this codebase.
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
