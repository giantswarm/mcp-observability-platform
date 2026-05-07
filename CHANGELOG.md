# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Tool surface completion (roadmap §0): `run_panel_query`, `get_query_examples`, and `get_panel_image` (delegated), plus local `list_silences` / `get_silence` (AM v2 silences via Grafana's datasource proxy, with optional `datasourceUid` override).
- `--disabled-tools` flag on the `serve` command (CSV of MCP tool names) skips matching tools at registration; surfaced as `tools.disabled` in the Helm chart so operators can drop e.g. `alerting_manage_rules` or `get_panel_image` without rebuilding.

### Changed

- Adopt `github.com/giantswarm/mcp-toolkit` v0.1.0 for cross-cutting plumbing. The bespoke `internal/server/middleware/{response_cap,timeout}.go` implementations and `internal/observability/tracing.go` are replaced by `responsecap.New`, `timeout.New`, `tracing.Init`. Logger construction switches to `logging.New`; the two-phase HTTP shutdown is now composed from two `httpx.Run` calls. Behaviour is preserved: response-cap default 128 KiB and tool timeout default 30s match the toolkit constants; the platform-specific cap hint (label matchers / sum/rate/topk advice) is set via `responsecap.Options.Hint`. The two-phase drain ordering (MCP first, observability second) is unchanged.
- `service.namespace=giantswarm.observability` and the K8s downward-API attrs (`POD_NAME`, `POD_NAMESPACE`, `NODE_NAME`) are now fed to the OTEL resource via `OTEL_RESOURCE_ATTRIBUTES` (the toolkit's tracing.Init reads them through `resource.WithFromEnv`). The Helm chart already exposes the downward-API env vars; merging happens in `cmd/serve.go` so existing deployments keep the same resource attribute set.

### Fixed

- `alerting_manage_rules` no longer 400s on multi-tenant Mimir setups: it now fans out across every datasource where Grafana's "Manage alerts" toggle is on (Mimir + Loki), tagging each entry by source. Pin a single datasource with `datasource_uid` to skip the fanout.
- `list_datasources`, `get_datasource`, and any future delegated upstream tool that goes through grafana-openapi-client-go's no-ctx convenience methods now respect the caller's `org`. `gfBinder` now caches one `mcp-grafana` `GrafanaClient` per resolved OrgID, so `OrgIDRoundTripper` ships the correct `X-Grafana-Org-Id` even when go-openapi's runtime falls back to `context.Background()` and the per-call ctx override path can't fire.

### Changed

- `grafana.Client.ListDatasources` is now cached per-OrgID with a 30s TTL (`LookupDatasourceByUID` inherits transparently), eliminating the extra Grafana RTT every alerting fanout and single-DS resolve previously paid.
- Single-DS tools (metrics, logs, Tempo, Alertmanager v2) resolve datasources via live `/api/datasources` instead of the CR-derived `Organization.Datasources` slice (`MatchKind`, `LookupDatasourceUIDByID`, `Organization.FindDatasource` are gone). Each tool gains an optional `datasourceUid` override so the LLM can scope a query to a specific tenant; default behaviour unchanged — first match by plugin type, i.e. the multi-tenant `gs-mimir` / `gs-loki` aggregate.
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
