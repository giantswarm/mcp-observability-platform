// Package tools wires the MCP tool surface of this MCP.
//
// Each category lives in its own file. Categories split into those
// delegated to upstream grafana/mcp-grafana (via gfBinder in
// grafanabind.go) and those handled locally:
//
//	dashboards.go  — delegated: dashboards, search, navigation, run_panel_query
//	metrics.go     — delegated: Mimir Prometheus tools (query, label/metric/value lists, metadata, histogram)
//	logs.go        — delegated: Loki tools (query, label names/values, stats, patterns)
//	alerting.go    — delegated: alerting_manage_rules (read meta-tool over alert rules)
//	examples.go    — delegated: get_query_examples (PromQL/LogQL/SQL syntax helper)
//	orgs.go        — list_orgs (local) + delegated list/get_datasource
//	alerts.go      — local: Alertmanager v2 alerts (no upstream equivalent)
//	silences.go    — local: Alertmanager v2 silences (no upstream equivalent)
//	tempo.go       — delegated to Tempo's own MCP server (/api/mcp) via Grafana datasource proxy
//
// gfBinder.bindOrgTool covers org-only delegated tools;
// gfBinder.bindDatasourceTool covers delegated tools that need a
// datasource UID — it resolves "org" → OrgID and picks the first live
// datasource of the requested plugin type, injecting its UID
// server-side. The datasourceUid arg stays in the schema as an
// optional override for callers who want to scope a query to a
// specific tenant; absent that override, the binder fills it.
//
// Shared helpers (datasource.go, pagination.go) carry the bits the
// remaining local handlers depend on. tools.go holds RegisterAll, the
// shared orgArg() helper, and cross-category constant tokens.
package tools
