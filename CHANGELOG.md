# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go MCP server scaffold: `/mcp` streamable-HTTP transport gated by mcp-oauth (Dex IdP), Grafana admin client, `GrafanaOrganization` CR-backed authz resolver, Prometheus metrics, OTel tracing, shared tool-handler helpers.
- Full MCP tool surface registered with the server: list_orgs / list_datasources / get_datasource, dashboards (list, get_summary, get_panel_queries, generate_deeplink, get_panel_image, get_annotations, run_panel_query, get_dashboard_property, get_dashboard_by_uid), Mimir (query_metrics, list_prometheus_metric_names, list_prometheus_label_names, list_prometheus_label_values, list_prometheus_metric_metadata, query_prometheus_histogram, list_alert_rules, get_alert_rule), Loki (query_logs, list_loki_label_names, list_loki_label_values, query_loki_patterns, query_loki_stats), Tempo (query_traces, query_tempo_metrics, list_tempo_tag_names, list_tempo_tag_values), Alertmanager (list_alerts, get_alert, list_silences), panel rendering (get_panel_image).
- Helm chart `mcp-observability-platform` with `Deployment` (with `checksum/config` rollout on ConfigMap changes), `Service`, `ServiceAccount`, `ClusterRole`+`Binding`, `ServiceMonitor`, `PodDisruptionBudget`, `NetworkPolicy` (ingress + configurable egress with auto-included kube-dns allow), `HorizontalPodAutoscaler` + `VerticalPodAutoscaler` (opt-in), and a runtime `ConfigMap` delivered to the pod via `envFrom` (timeouts, response cap, rate-limit thresholds, resolver TTL, OAuth refresh window). `values.schema.json`, helm-unittest specs under `tests/`, example overlays (memory, valkey, rbac-minimal, autoscaling), and a `README.md.gotmpl` for helm-docs. OAuth public client registration is `false` by default to match the server-side safe default.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
