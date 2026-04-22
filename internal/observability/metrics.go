// Package observability owns the Prometheus metrics and /metrics HTTP handler
// for this MCP. Metrics are registered on a package-local registry (not the
// global default) so tests can instantiate the server twice without hitting
// duplicate-registration panics, and so multiple MCP instances in one binary
// never cross-pollute metric streams.
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

// ToolCallTotal counts every MCP tool invocation by name and outcome.
// Outcome is one of "ok" | "err". Cardinality is bounded by the number of
// registered tools.
var ToolCallTotal = promauto.With(registry).NewCounterVec(
	prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "tool_call_total",
		Help:      "Number of MCP tool calls, labeled by tool name and outcome (ok|err).",
	},
	[]string{"tool", "outcome"},
)

// ToolCallDuration measures per-tool handler latency. Buckets cover the
// real handler range: cached CR lookups (~50 ms) up to panel renders
// (~60 s broad LogQL / slow datasource proxy). The previous
// ExponentialBuckets(0.01, 2.5, 10) jumped from 38 s straight to +Inf,
// so p99 above 38 s was unmeasurable.
var ToolCallDuration = promauto.With(registry).NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "tool_call_duration_seconds",
		Help:      "Duration of MCP tool handlers, by tool name and outcome.",
		Buckets:   []float64{0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	},
	[]string{"tool", "outcome"},
)

// GrafanaProxyTotal counts datasource-proxy calls by downstream path so we can
// see which observability backend (Mimir/Loki/Tempo/AM) is most used.
var GrafanaProxyTotal = promauto.With(registry).NewCounterVec(
	prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "grafana_proxy_total",
		Help:      "Number of Grafana datasource-proxy calls, labeled by downstream path.",
	},
	[]string{"path"},
)

// GrafanaProxyDuration measures per-path Grafana proxy latency. The path
// label is the downstream API path (bounded cardinality: one label value per
// registered tool). The status label distinguishes success from error so
// ops can see tail-latency changes during incidents — before this split,
// the error path skipped Observe entirely and half the signal vanished.
var GrafanaProxyDuration = promauto.With(registry).NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "grafana_proxy_duration_seconds",
		Help:      "Duration of Grafana datasource-proxy calls, by downstream path and status.",
		// Proxy calls are typically sub-second (simple Mimir/Loki queries
		// with label matchers); DefBuckets' top bucket of 10s is plenty
		// and gives dense resolution in the 5 ms–1 s range where
		// regressions are most visible.
		Buckets: prometheus.DefBuckets,
	},
	[]string{"path", "status"},
)

// OrgCacheSize reports the number of GrafanaOrganization CRs currently cached.
// Updated periodically by the authz layer.
var OrgCacheSize = promauto.With(registry).NewGauge(
	prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "org_cache_size",
		Help:      "Number of GrafanaOrganization CRs in the informer cache.",
	},
)

// MetricsHandler returns an http.Handler that serves /metrics in Prometheus
// text format from the package-local registry.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
