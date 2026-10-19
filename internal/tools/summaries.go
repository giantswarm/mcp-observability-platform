package tools

import (
	"fmt"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/mark3labs/mcp-go/mcp"
)

// upstreamSummaries leads each delegated tool whose upstream description
// opens with a line longer than tooldesc.SummaryMax: muster's discovery
// shows only that line. The upstream text follows unchanged.
var upstreamSummaries = map[string]string{
	"diff_tempo_traces":               "Compare two Tempo traces: a compact summary of what differs, plus the span-level patch when it is small.",
	"generate_deeplink":               "Build a Grafana link to a dashboard, panel or Explore query, optionally as a short URL.",
	"get_dashboard_by_uid":            "Get a whole Grafana dashboard by UID, or a saved version: panels, variables, settings. Can be very large.",
	"get_dashboard_panel_queries":     "Get the queries of a Grafana dashboard's panels, or of one panel, optionally with variables substituted.",
	"get_dashboard_property":          "Get parts of a Grafana dashboard by JSONPath, e.g. panel titles or queries, without the whole JSON.",
	"get_dashboard_summary":           "Get a compact overview of a Grafana dashboard: title, panel count and types, variables; not the full JSON.",
	"get_datasource":                  "Get one Grafana datasource by UID or name: type, URL, access settings and JSON data.",
	"get_panel_image":                 "Render a Grafana panel, dashboard or Explore view as a PNG image; needs the Grafana Image Renderer.",
	"get_query_examples":              "Get example queries for a datasource type: PromQL, LogQL, ClickHouse SQL, CloudWatch, InfluxDB.",
	"get_tempo_traceql_docs":          "Get the TraceQL reference with examples; read it before a non-trivial TraceQL query or after one fails.",
	"list_datasources":                "List the org's Grafana datasources with their UIDs, filtered by type or name, paged.",
	"list_loki_label_names":           "List the label names in the org's Loki logs over a time range (default the last hour).",
	"list_loki_label_values":          "List the values of one Loki log label over a time range (default the last hour), optionally per stream selector.",
	"list_prometheus_label_names":     "List the label names in the org's Mimir metrics, optionally for a series selector and time range.",
	"list_prometheus_label_values":    "List the values of one Prometheus label in the org's Mimir metrics, optionally per selector and time range.",
	"list_prometheus_metric_metadata": "List Prometheus metric metadata for the metrics currently scraped from targets.",
	"list_prometheus_metric_names":    "List the metric names in the org's Mimir metrics, filtered by regex and paged, optionally for a time range.",
	"list_tempo_attribute_names":      "List the attribute names available to TraceQL queries; narrow by scope (resource, span), or the list is large.",
	"list_tempo_attribute_values":     "List the values of one fully scoped Tempo attribute, e.g. resource.service.name.",
	"query_loki_logs":                 "Run a LogQL query on the org's Loki logs: log lines (newest first by default) or metric samples.",
	"query_loki_patterns":             "Get the common log patterns, with counts, for a Loki stream selector over a time range.",
	"query_loki_stats":                "Get index statistics for a Loki stream selector: streams, chunks, index entries, bytes; not log line counts.",
	"query_prometheus":                "Run a PromQL query, instant or range, on the org's Mimir metrics.",
	"query_tempo_metrics":             "Compute metrics from traces with a TraceQL metrics query, instant or range (default).",
	"run_panel_query":                 "Run the queries of one or more Grafana dashboard panels, with variables and macros substituted.",
	"search_dashboards":               "Search Grafana dashboards by query, folder, tag or starred: title, UID, folder, tags and URL.",
}

// withSummary leads t's upstream description with its upstreamSummaries
// entry, if any.
func withSummary(t mcp.Tool) mcp.Tool {
	summary, ok := upstreamSummaries[t.Name]
	if !ok {
		return t
	}
	d, err := tooldesc.Description(summary, t.Description)
	if err != nil {
		panic(fmt.Sprintf("tools: tool %q: %v", t.Name, err))
	}
	t.Description = d
	return t
}
