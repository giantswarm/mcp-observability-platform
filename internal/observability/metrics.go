package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace is the Prometheus metric prefix for everything this process
// emits. Short so rule writers don't have to drag a 22-char prefix through
// every alert; distinctive enough not to collide with other Giant Swarm
// MCP servers in a shared scrape target (the `service.name` label
// disambiguates).
const namespace = "mcp"

// registry is the package-local registry backing every metric in this
// package. We own it (rather than using prometheus.DefaultRegisterer) so
// tests can instantiate the server twice without duplicate-registration
// panics, and multiple MCP instances in one binary never cross-pollute.
//
// Go runtime + process collectors are registered explicitly because the
// default registerer is what normally provides them; moving off it means
// we must re-add them or operators lose `go_gc_*`, `process_resident_memory_bytes`
// and friends.
var registry = func() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return r
}()

// ToolCallTotal counts every MCP tool invocation by name. Pair with
// ToolCallErrorsTotal for an error rate (HTTP-server idiom).
var ToolCallTotal = promauto.With(registry).NewCounterVec(
	prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "tool_call_total",
		Help:      "Number of MCP tool calls, labeled by tool name.",
	},
	[]string{"tool"},
)

// ToolCallErrorsTotal counts tool invocations that returned a Go error
// or an IsError result. PromQL: rate(mcp_tool_call_errors_total[5m]) /
// rate(mcp_tool_call_total[5m]) for the error rate.
var ToolCallErrorsTotal = promauto.With(registry).NewCounterVec(
	prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "tool_call_errors_total",
		Help:      "Number of MCP tool calls that returned an error, labeled by tool name.",
	},
	[]string{"tool"},
)

var ToolCallDuration = promauto.With(registry).NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "tool_call_duration_seconds",
		Help:      "Duration of MCP tool handlers, by tool name.",
		Buckets:   prometheus.DefBuckets,
	},
	[]string{"tool"},
)

// MetricsHandler returns an http.Handler that serves /metrics in Prometheus
// text format from the package-local registry.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
