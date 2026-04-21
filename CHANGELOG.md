# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go MCP server scaffold: `/mcp` streamable-HTTP transport gated by mcp-oauth (Dex IdP), Grafana admin client, `GrafanaOrganization` CR-backed authz resolver, Prometheus metrics, OTel tracing, shared tool-handler helpers.
- Full MCP tool surface registered with the server: list_orgs / list_datasources / get_datasource, dashboards (list, get_summary, get_panel_queries, generate_deeplink, get_panel_image, get_annotations, run_panel_query, get_dashboard_property, get_dashboard_by_uid), Mimir (query_metrics, list_prometheus_metric_names, list_prometheus_label_names, list_prometheus_label_values, list_prometheus_metric_metadata, query_prometheus_histogram, list_alert_rules, get_alert_rule), Loki (query_logs, list_loki_label_names, list_loki_label_values, query_loki_patterns, query_loki_stats), Tempo (query_traces, query_tempo_metrics, list_tempo_tag_names, list_tempo_tag_values), Alertmanager (list_alerts, get_alert, list_silences), panel rendering (get_panel_image).
- GitHub Actions CI workflow (`ci.yaml`) running Go test + vet, yamllint, Helm lint, helm-unittest, and govulncheck on every PR and push to main. Runs alongside the devctl-generated workflows (`zz_generated.*`) already in the repo.
- `Makefile.custom.mk` with CI-facing targets (`check`, `test-vet`, `helm-lint`, `helm-test`, `govulncheck`, `lint-yaml`) + developer conveniences (`tidy`, `helm-template`).

### Changed

- `.circleci/config.yml` expanded from chart-only publish to the full `mcp-kubernetes` pattern: `architect/go-build`, multi-arch image push (amd64 on branches, multi-arch on tags to gsoci + all registries including China mirrors), and `run-tests-with-ats` for chart ATS tests.
- `renovate.json5` extended with the `lang-go.json5` preset.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
