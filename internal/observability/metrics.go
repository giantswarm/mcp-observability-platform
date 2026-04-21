// Package observability owns the Prometheus metrics and /metrics HTTP handler
// for this MCP. Counters/gauges are registered on the default registry so
// promhttp.Handler() serves them without extra wiring.
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "mcp_observability_platform"

// ToolCallTotal counts every MCP tool invocation by name and outcome.
// Outcome is one of "ok" | "err". Cardinality is bounded by the number of
// registered tools.
var ToolCallTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "tool_call_total",
		Help:      "Number of MCP tool calls, labeled by tool name and outcome (ok|err).",
	},
	[]string{"tool", "outcome"},
)

// ToolCallDuration measures per-tool handler latency. Buckets chosen to cover
// both fast (cached CR reads) and slow (broad LogQL) tool calls.
var ToolCallDuration = promauto.NewHistogramVec(
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
var GrafanaProxyTotal = promauto.NewCounterVec(
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
var GrafanaProxyDuration = promauto.NewHistogramVec(
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
var OrgCacheSize = promauto.NewGauge(
	prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "org_cache_size",
		Help:      "Number of GrafanaOrganization CRs in the informer cache.",
	},
)

// MetricsHandler returns an http.Handler that serves /metrics in Prometheus
// text format from the default registry.
func MetricsHandler() http.Handler { return promhttp.Handler() }
