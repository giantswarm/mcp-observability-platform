// Package tools wires the MCP tool surface of this MCP.
//
// Each category lives in its own file. Categories split into those
// delegated to upstream grafana/mcp-grafana (via gfBinder in
// grafanabind.go) and those handled locally:
//
//	dashboards.go  — delegated: dashboards, search, navigation
//	metrics.go     — delegated: Mimir Prometheus tools (query, label/metric/value lists, histogram)
//	logs.go        — delegated: Loki tools (query, label names/values, stats, patterns)
//	alerting.go    — delegated: alerting_manage_rules (read meta-tool over alert rules)
//	orgs.go        — list_orgs (local) + delegated list/get_datasource
//	alerts.go      — local: Alertmanager v2 alerts (no upstream equivalent)
//	traces.go      — local: query_traces + Tempo tag discovery (upstream has no Tempo surface)
//
// gfBinder.bindOrgTool covers org-only delegated tools;
// gfBinder.bindDatasourceTool covers delegated tools that need a
// datasource UID — it resolves "org" → OrgID + datasource UID
// server-side so the LLM never sees a datasourceUid argument.
//
// Shared helpers (datasource.go, pagination.go) carry the bits the
// remaining local handlers depend on. tools.go holds RegisterAll, the
// shared orgArg() helper, and the per-category constant tokens.
package tools
