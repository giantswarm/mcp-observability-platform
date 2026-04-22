# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Go MCP server** over streamable-HTTP (default), SSE, or stdio. OAuth 2.1 via `mcp-oauth` with Dex as IdP. Authorization resolved from OIDC
  groups against `GrafanaOrganization.spec.rbac.{admins,editors,viewers} via Grafana's own SSO `org_mapping` evaluation — we don't re-implement
  group-matching semantics locally.
- **32+ read-only MCP tools** across orgs, datasources, dashboards (including `search_dashboards` + `search_folders`), Mimir PromQL
  (`query_prometheus`, histograms, metadata, labels, rules), Loki (`query_loki_logs`, stats, patterns, labels), Tempo TraceQL, Alertmanager
  (`list_alerts`/`get_alert`/`list_silences`), panel rendering, and annotations (`get_annotations` + `get_annotation_tags`). Tool names
  align with `grafana/mcp-grafana` where they overlap so LLMs trained on upstream docs hit them on first try.
- **Uniform response cap** (`TOOL_MAX_RESPONSE_BYTES`, default 128 KiB) returns a structured `response_too_large` payload so LLM clients can
  react instead of silently truncating.
- **Per-org tenant/datasource selection** via `GrafanaOrganization` CR metadata, with the multi-tenant header already baked into each
  datasource by observability-operator — the MCP picks the datasource, Grafana applies the tenant.
- **Prometheus metrics** on `:9091/metrics` (namespace `mcp_*`; realistic latency buckets from 25 ms to 60 s; see README), OTel tracing
  via `OTEL_EXPORTER_OTLP_*`, OTLP logs via the `otelslog` bridge when
  `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is set (every slog record picks up `trace_id`/`span_id` for jump-from-span-to-log correlation), and
  a structured audit log (`{timestamp, caller, caller_token_source, tool, args, outcome, duration_ms, error}`) on stderr — always on,
  stable schema, ingestable by SIEM. Args strings >4 KiB are truncated with a `…[truncated N bytes]` marker and the whole args map is
  replaced with `{truncated: true, bytes: N}` if the total exceeds 16 KiB, so audit lines always stay well under Loki's 256 KiB ingest
  ceiling. One `Instrument` middleware feeds audit + metrics + span attribute from a single `Classify()` call so cross-signal
  correlation never drifts.
- **Deep readiness** (`/readyz` probes Grafana + Dex + K8s informer with 2s per-check deadline; `/healthz/detailed` returns JSON summary) and
  two-phase graceful shutdown (MCP drains first, observability stays answering probes during drain).
- **Hardened OAuth config** — `MCP_OAUTH_TRUSTED_AUDIENCES` for SSO token forwarding (muster / sibling MCPs), `MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES`
  for CLI clients (`cursor://`, `vscode://`), encryption-key entropy check rejecting placeholder / all-zero keys, HTTPS-only issuer URLs
  (opt-out for local dev via `MCP_OAUTH_ALLOW_INSECURE_HTTP`).
- **Helm chart** with runtime ConfigMap (toolTimeout, response cap, rate-limit thresholds seeded for future PR 13, OAuth refresh window,
  trusted audiences/schemes), NetworkPolicy, PDB, HPA/VPA, four example overlays (memory / valkey / rbac-minimal / autoscaling), and 19+
  helm-unittest tests.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
