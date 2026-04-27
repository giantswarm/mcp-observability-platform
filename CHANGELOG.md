# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- 37 read-only MCP tools across Grafana / Mimir / Loki / Tempo / Alertmanager, plus three SRE-triage co-pilots (`find_error_pattern_logs`, `find_slow_requests`, `explain_query`).
- OAuth 2.1 via `mcp-oauth` (Dex IdP); multi-tenant authz derived from `GrafanaOrganization` CR membership; fail-closed `RequireCaller` middleware.
- Three transports: `streamable-http` (default), `sse`, `stdio`. Per-tool timeout, response-size cap with structured `response_too_large` payload, structured per-call audit stream.
- Prometheus metrics (`mcp_*` namespace), OTEL tracing, and OTLP logs with `trace_id`/`span_id` correlation.
- Helm chart with NetworkPolicy, HPA, VPA, PDB, ServiceMonitor, and four example overlays.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
