package observability

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetricsHandler_IsReusable guards against regressions where the handler
// is bound to state that cannot be served twice (e.g. promauto on the global
// DefaultRegisterer panicking on a second init).
func TestMetricsHandler_IsReusable(t *testing.T) {
	for range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/metrics", nil)
		MetricsHandler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
}

// TestMetricsHandler_ServesCustomAndDefaultCollectors asserts that moving off
// the default Prometheus registerer did not silently drop the Go runtime /
// process collectors that operators rely on.
func TestMetricsHandler_ServesCustomAndDefaultCollectors(t *testing.T) {
	ToolCallTotal.WithLabelValues("probe").Inc()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	got := string(body)

	for _, needle := range []string{
		"mcp_tool_call_total",           // custom counter
		"go_goroutines",                 // Go runtime collector
		"process_resident_memory_bytes", // process collector
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("%s missing from /metrics output", needle)
		}
	}
}
