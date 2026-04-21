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

const namespace = "mcp_observability_platform"

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

// ToolCallDuration measures per-tool handler latency. Buckets chosen to cover
// both fast (cached CR reads) and slow (broad LogQL) tool calls.
var ToolCallDuration = promauto.With(registry).NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "tool_call_duration_seconds",
		Help:      "Duration of MCP tool handlers, by tool name and outcome.",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2.5, 10), // 10ms → ~96s
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
// registered tool), which makes this safe to aggregate by backend.
var GrafanaProxyDuration = promauto.With(registry).NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "grafana_proxy_duration_seconds",
		Help:      "Duration of Grafana datasource-proxy calls, by downstream path.",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2.5, 10),
	},
	[]string{"path"},
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
