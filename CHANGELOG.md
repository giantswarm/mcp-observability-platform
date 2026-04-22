# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Pre-release work — everything here will roll up into the v0.1.0 release
notes. For per-PR detail see the `git log`; `docs/roadmap.md` tracks what's
landed vs what's still in the pre-release cleanup plan.

### What v0.1.0 will ship

- **Go MCP server** over streamable-HTTP (default), SSE, or stdio. OAuth
  2.1 via `mcp-oauth` with Dex as IdP. Authorization resolved from OIDC
  groups against `GrafanaOrganization.spec.rbac.{admins,editors,viewers}`
  via Grafana's own SSO `org_mapping` evaluation — we don't re-implement
  group-matching semantics locally.
- **32+ read-only MCP tools** across orgs, datasources, dashboards
  (including `search_dashboards` + `search_folders`), Mimir PromQL
  (`query_prometheus`, histograms, metadata, labels, rules), Loki
  (`query_loki_logs`, stats, patterns, labels), Tempo TraceQL, Alertmanager
  (`list_alerts`/`get_alert`/`list_silences`), panel rendering, and
  annotations (`get_annotations` + `get_annotation_tags`). Tool names
  align with `grafana/mcp-grafana` where they overlap so LLMs trained on
  upstream docs hit them on first try.
- **Uniform response cap** (`TOOL_MAX_RESPONSE_BYTES`, default 128 KiB)
  returns a structured `response_too_large` payload so LLM clients can
  react instead of silently truncating.
- **Per-org tenant/datasource selection** via `GrafanaOrganization` CR
  metadata, with the multi-tenant header already baked into each
  datasource by observability-operator — the MCP picks the datasource,
  Grafana applies the tenant.
- **Prometheus metrics** on `:9091/metrics` (namespace `mcp_*`; see
  README), OTel tracing via `OTEL_EXPORTER_OTLP_*`, and a structured
  audit log (`{timestamp, caller, tool, args, outcome, duration_ms,
  error}`) on stderr — always on, stable schema, ingestable by SIEM.
  Shared `Classify()` feeds audit + metrics + span attribute so cross-
  signal correlation never drifts.
- **Deep readiness** (`/readyz` probes Grafana + Dex + K8s informer with
  2s per-check deadline; `/healthz/detailed` returns JSON summary) and
  two-phase graceful shutdown (MCP drains first, observability stays
  answering probes during drain).
- **Hardened OAuth config** — `MCP_OAUTH_TRUSTED_AUDIENCES` for SSO
  token forwarding (muster / sibling MCPs), `MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES`
  for CLI clients (`cursor://`, `vscode://`), encryption-key entropy
  check rejecting placeholder / all-zero keys, HTTPS-only issuer URLs
  (opt-out for local dev via `MCP_OAUTH_ALLOW_INSECURE_HTTP`).
- **Resolver cache**: bounded LRU, keyed on OIDC `sub` (not spoofable
  email), singleflight-collapsed stampedes, split positive/negative
  TTLs (30s / 5s) so mid-SSO-outage failures don't lock users out,
  slice-cloned returns so handler mutations can't escape into the cache.
  Distinct `ErrOrgNotFound` vs `ErrNotAuthorised` for operator debugging.
- **Helm chart** with runtime ConfigMap (toolTimeout, response cap,
  rate-limit thresholds seeded for future PR 13, OAuth refresh window,
  trusted audiences/schemes), NetworkPolicy, PDB, HPA/VPA, four example
  overlays (memory / valkey / rbac-minimal / autoscaling), and 19+
  helm-unittest tests.
- **CI**: CircleCI (via `architect` orb) publishes multi-arch images +
  chart in the `mcp-kubernetes` shape; GitHub Actions runs `go test`,
  `yamllint`, `helm lint`, `helm-unittest`, `govulncheck` per PR.
- **Release flow**: manual `release#vX.Y.Z` branch → auto-opened
  CHANGELOG-promotion PR → tag → CircleCI publishes image + chart.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
