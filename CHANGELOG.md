# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `alerting_manage_rules` no longer 400s on multi-tenant Mimir setups: it now fans out across every datasource where Grafana's "Manage alerts" toggle is on (Mimir + Loki), tagging each entry by source. Pin a single datasource with `datasource_uid` to skip the fanout.
- `list_datasources`, `get_datasource`, and any future delegated upstream tool that goes through grafana-openapi-client-go's no-ctx convenience methods now respect the caller's `org`. `gfBinder` now caches one `mcp-grafana` `GrafanaClient` per resolved OrgID, so `OrgIDRoundTripper` ships the correct `X-Grafana-Org-Id` even when go-openapi's runtime falls back to `context.Background()` and the per-call ctx override path can't fire.

### Changed

- Tempo tools delegate to Tempo's own MCP server (`/api/mcp`) via Grafana's datasource proxy. `query_traces` and `list_tempo_tag_*` become `traceql-search`, `get-trace`, `get-attribute-names`, `get-attribute-values`, `traceql-metrics-instant`, `traceql-metrics-range`, `docs-traceql`. Requires `tempo-app` chart with `query_frontend.mcp_server.enabled=true`; if not reachable at startup the binder logs a warning and skips registration.

## [0.1.0] - 2026-04-29

### Added

- Read-only MCP tool surface across Grafana, Mimir, Loki, Tempo, and Alertmanager v2. Most tools delegate to upstream `grafana/mcp-grafana` via `gfBinder` (`internal/tools/grafanabind.go`), which adds the `org` argument and resolves OrgID + datasource UID server-side; Tempo, Alertmanager v2, and `list_orgs` stay local.
- OAuth 2.1 via `mcp-oauth` (Dex IdP); multi-tenant authz derived from `GrafanaOrganization` CR membership; fail-closed `RequireCaller` middleware on top of upstream-validated bearers.
- Three transports — `streamable-http` (default), `sse`, `stdio` — sharing one HTTP listener with `/metrics`, `/healthz`, and `/readyz`. Per-tool timeout, response-size cap with structured `response_too_large` payload, and a `tool_call` audit slog line per invocation.
- Prometheus tool-call counter + error counter + duration histogram, OTEL tracing (no-op without `OTEL_EXPORTER_OTLP_ENDPOINT`), and `trace_id` / `span_id` on every audit line for log-trace correlation.
- Helm chart with NetworkPolicy, HPA, VPA, PDB, ServiceMonitor, and example overlays for memory- and Valkey-backed OAuth storage.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/giantswarm/mcp-observability-platform/releases/tag/v0.1.0
