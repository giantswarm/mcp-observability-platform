# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Read-only MCP tool surface across Grafana, Mimir, Loki, Tempo, and Alertmanager v2. Most tools delegate to upstream `grafana/mcp-grafana` via `internal/tools/upstream.Registrar`, which adds the `org` argument and resolves OrgID + datasource UID server-side; Tempo, Alertmanager v2, and `list_orgs` stay local.
- OAuth 2.1 via `mcp-oauth` (Dex IdP); multi-tenant authz derived from `GrafanaOrganization` CR membership; fail-closed `RequireCaller` middleware on top of upstream-validated bearers.
- Three transports — `streamable-http` (default), `sse`, `stdio` — sharing one HTTP listener with `/metrics`, `/healthz`, and `/readyz`. Per-tool timeout, response-size cap with structured `response_too_large` payload, and a `tool_call` audit slog line per invocation.
- Prometheus tool-call counter + error counter + duration histogram, OTEL tracing (no-op without `OTEL_EXPORTER_OTLP_ENDPOINT`), and `trace_id` / `span_id` on every audit line for log-trace correlation.
- Helm chart with NetworkPolicy, HPA, VPA, PDB, ServiceMonitor, and example overlays for memory- and Valkey-backed OAuth storage.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
