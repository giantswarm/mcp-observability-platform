// Package tools wires the MCP tool surface of this MCP.
//
// Tools split between bridged (delegated to upstream
// grafana/mcp-grafana) and local. Each category lives in its own file:
//
//	dashboards.go  — bridged: dashboards, search, annotations, deeplinks, panel rendering
//	metrics.go     — bridged: Mimir Prometheus tools (query, label/metric/value lists, histogram)
//	logs.go        — bridged: Loki tools (query, label names/values, stats, patterns)
//	alerting.go    — bridged: alerting_manage_rules (read meta-tool over alert rules)
//	orgs.go        — list_orgs (local) + bridged list/get_datasource
//	alerts.go      — local: Alertmanager v2 alerts (no upstream equivalent)
//	silences.go    — local: Alertmanager silences (no upstream equivalent)
//	traces.go      — local: Tempo TraceQL + tag tools (upstream has no Tempo surface)
//	triage.go      — local: find_error_pattern_logs / find_slow_requests
//	                 mimicking the upstream Sift API on open-source primitives,
//	                 plus explain_query (no upstream equivalent)
//
// Bridging happens through internal/tools/upstream: WithOrg /
// WithOrgReplacingDatasource on the schema side, Bridge.Wrap /
// Bridge.WrapDatasource on the handler side. The bridge resolves
// "org" → OrgID + datasource UID server-side so the LLM never sees a
// datasourceUid argument.
//
// Shared helpers (datasource.go, pagination.go) carry the bits the
// remaining local handlers depend on. tools.go holds RegisterAll and
// the per-category constant tokens.
package tools
