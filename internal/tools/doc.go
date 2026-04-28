// Package tools wires the MCP tool surface of this MCP.
//
// Tools split between bridged (delegated to upstream
// grafana/mcp-grafana) and local. Each category lives in its own file:
//
//	dashboards.go  — bridged: dashboards, search, navigation
//	metrics.go     — bridged: Mimir Prometheus tools (query, label/metric/value lists, histogram)
//	logs.go        — bridged: Loki tools (query, label names/values, stats, patterns)
//	alerting.go    — bridged: alerting_manage_rules (read meta-tool over alert rules)
//	orgs.go        — list_orgs (local) + bridged list/get_datasource
//	alerts.go      — local: Alertmanager v2 alerts (no upstream equivalent)
//	traces.go      — local: query_traces + Tempo tag discovery (upstream has no Tempo surface)
//
// Bridged tools are wired through the unexported gfBinder
// (grafanabind.go). bindOrgTool covers org-only tools; bindDatasourceTool
// covers tools that need a datasource UID — it resolves "org" → OrgID
// + datasource UID server-side so the LLM never sees a datasourceUid
// argument.
//
// Shared helpers (datasource.go, pagination.go) carry the bits the
// remaining local handlers depend on. tools.go holds RegisterAll, the
// shared orgArg() helper, and the per-category constant tokens.
package tools
