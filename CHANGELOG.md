# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go MCP server scaffold: `/mcp` streamable-HTTP transport gated by mcp-oauth (Dex IdP), Grafana admin client, `GrafanaOrganization` CR-backed authz resolver, Prometheus metrics, OTel tracing, shared tool-handler helpers.
- Full MCP tool surface registered with the server: list_orgs / list_datasources / get_datasource, dashboards (list, get_summary, get_panel_queries, generate_deeplink, get_panel_image, get_annotations, run_panel_query, get_dashboard_property, get_dashboard_by_uid), Mimir (query_metrics, list_prometheus_metric_names, list_prometheus_label_names, list_prometheus_label_values, list_prometheus_metric_metadata, query_prometheus_histogram, list_alert_rules, get_alert_rule), Loki (query_logs, list_loki_label_names, list_loki_label_values, query_loki_patterns, query_loki_stats), Tempo (query_traces, query_tempo_metrics, list_tempo_tag_names, list_tempo_tag_values), Alertmanager (list_alerts, get_alert, list_silences), panel rendering (get_panel_image). MCP resources (org, dashboard, alert URIs) and an `investigate-alert` prompt template.
- `internal/tools/middleware` package: composable `Middleware` primitives (`Chain`, `Default`, `Tracing`, `Metrics`) plus shared helpers (arg extraction, response-cap, per-tool timeout, pagination, datasource proxy, caller context) that every tool registration goes through. Feature PRs add new concerns (audit, progress/cancellation, rate limit) by appending to `Default` — tool handlers never touch cross-cutting plumbing.
- MCP tool annotations (`readOnlyHint`, `idempotentHint`, `openWorldHint`, `destructiveHint: false`) emitted on every tool so clients can reason about safety and retry semantics.

### Changed

- `app.giantswarm.io` label group was changed to `application.giantswarm.io`

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
