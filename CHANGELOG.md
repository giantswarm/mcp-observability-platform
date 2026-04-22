# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Work in progress toward v0.1.0. Shipped so far:

- Go MCP server with `streamable-http` (default), `sse`, and `stdio` transports.
- OAuth 2.1 via `mcp-oauth` with Dex; authz derived from `GrafanaOrganization` CR membership.
- 32+ read-only MCP tools across orgs, datasources, dashboards, Mimir PromQL, Loki, Tempo, Alertmanager, panel rendering, and annotations.
- Uniform `response_too_large` payload (`TOOL_MAX_RESPONSE_BYTES`, default 128 KiB).
- Prometheus metrics (`mcp_*` namespace), OTel tracing, and OTLP logs via the `otelslog` bridge for trace↔log correlation.
- Structured audit stream with `caller_token_source`, a 4 KiB per-value / 16 KiB total args size cap, pluggable redactor.
- Deep readiness (`/readyz` probes Grafana + Dex + K8s informer) and two-phase graceful shutdown.
- Helm chart with runtime ConfigMap, NetworkPolicy, PDB, HPA/VPA, and four example overlays.

See `git log` for the per-PR history.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
