package tools

import (
	"fmt"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/mark3labs/mcp-go/mcp"
)

// upstreamSummary leads a delegated tool whose upstream description opens
// with a line longer than tooldesc.SummaryMax: muster's discovery shows only
// that line. The upstream text follows unchanged.
type upstreamSummary struct {
	summary string
	// upstream pins the upstream description and input schema the summary
	// was written against (upstreamHash); a test fails when they change.
	upstream string
}

var upstreamSummaries = map[string]upstreamSummary{
	"diff_tempo_traces":               {"Compare two Tempo traces: a compact summary of what differs, plus the span-level patch when it is small.", "6eaf9fce29e6"},
	"generate_deeplink":               {"Build a Grafana link to a dashboard, panel or Explore query, optionally as a short URL.", "3dec54b8b0fe"},
	"get_dashboard_by_uid":            {"Get a whole Grafana dashboard by UID, or a saved version: panels, variables, settings. Can be very large.", "41996d87674a"},
	"get_dashboard_panel_queries":     {"Get the queries of a Grafana dashboard's panels, or of one panel, optionally with variables substituted.", "3974fced2930"},
	"get_dashboard_property":          {"Get parts of a Grafana dashboard by JSONPath, e.g. panel titles or queries, without the whole JSON.", "699edd2b879c"},
	"get_dashboard_summary":           {"Get a compact overview of a Grafana dashboard: title, panel count and types, variables; not the full JSON.", "4975a90d6855"},
	"get_datasource":                  {"Get one Grafana datasource by UID or name: type, URL, access settings and JSON data.", "e00d9fa41e72"},
	"get_panel_image":                 {"Render a Grafana panel, dashboard or Explore view as a PNG image; needs the Grafana Image Renderer.", "c33f87af0ef6"},
	"get_query_examples":              {"Get example queries for a datasource type: PromQL, LogQL, ClickHouse SQL, CloudWatch, InfluxDB.", "3183b2fe50f5"},
	"get_tempo_traceql_docs":          {"Get the TraceQL reference with examples; read it before a non-trivial TraceQL query or after one fails.", "52b0356f6086"},
	"list_datasources":                {"List the org's Grafana datasources with their UIDs, optionally filtered by type or name, paged.", "5b9bb50c1711"},
	"list_loki_label_names":           {"List the label names in the org's Loki logs over a time range (default the last hour).", "2d862d4246d1"},
	"list_loki_label_values":          {"List the values of one Loki log label over a time range (default the last hour), optionally per stream selector.", "d7f1b59ae0c2"},
	"list_prometheus_label_names":     {"List the label names in the org's Mimir metrics, optionally for a series selector and time range.", "ad6aa31a51d6"},
	"list_prometheus_label_values":    {"List the values of one Prometheus label in the org's Mimir metrics, optionally per selector and time range.", "e9317b564d3a"},
	"list_prometheus_metric_metadata": {"List Prometheus metric metadata for the metrics currently scraped from targets.", "d54d46df38cd"},
	"list_prometheus_metric_names":    {"List the metric names in the org's Mimir metrics, optionally by regex and time range, paged.", "e02ff131ab86"},
	"list_tempo_attribute_names":      {"List the attribute names available to TraceQL queries; narrow by scope (resource, span), or the list is large.", "766234dd3317"},
	"list_tempo_attribute_values":     {"List the values of one fully scoped Tempo attribute, e.g. resource.service.name.", "714b18b4b641"},
	"query_loki_logs":                 {"Run a LogQL query on the org's Loki logs: log lines (newest first by default) or metric samples.", "3d2e395fc380"},
	"query_loki_patterns":             {"Get the common log patterns, with counts, for a Loki stream selector over a time range.", "b127e9da2d9a"},
	"query_loki_stats":                {"Get index statistics for a Loki stream selector: streams, chunks, index entries, bytes; not log line counts.", "0b6f8ff4eaf5"},
	"query_prometheus":                {"Run a PromQL query, instant or range, on the org's Mimir metrics.", "861e0ccd9541"},
	"query_tempo_metrics":             {"Compute metrics from traces with a TraceQL metrics query, instant or range (default).", "4ccf7370c92d"},
	"run_panel_query":                 {"Run one query (default the first) of each given Grafana dashboard panel, with variables and macros substituted.", "779302fc5663"},
	"search_dashboards":               {"Search Grafana dashboards by query, folder, tag or starred: title, UID, folder, tags and URL.", "a0d5287bab94"},
}

// withSummary leads t's upstream description with its upstreamSummaries
// entry, if any.
func withSummary(t mcp.Tool) mcp.Tool {
	us, ok := upstreamSummaries[t.Name]
	if !ok {
		return t
	}
	d, err := tooldesc.Description(us.summary, t.Description)
	if err != nil {
		panic(fmt.Sprintf("tools: tool %q: %v", t.Name, err))
	}
	t.Description = d
	return t
}
