# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go MCP server scaffold: `/mcp` streamable-HTTP transport gated by mcp-oauth (Dex IdP), Grafana admin client, `GrafanaOrganization` CR-backed authz resolver, Prometheus metrics, OTel tracing, shared tool-handler helpers.
- Full MCP tool surface registered with the server: list_orgs / list_datasources / get_datasource, dashboards (list, get_summary, get_panel_queries, generate_deeplink, get_panel_image, get_annotations, run_panel_query, get_dashboard_property, get_dashboard_by_uid), Mimir (query_metrics, list_prometheus_metric_names, list_prometheus_label_names, list_prometheus_label_values, list_prometheus_metric_metadata, query_prometheus_histogram, list_alert_rules, get_alert_rule), Loki (query_logs, list_loki_label_names, list_loki_label_values, query_loki_patterns, query_loki_stats), Tempo (query_traces, query_tempo_metrics, list_tempo_tag_names, list_tempo_tag_values), Alertmanager (list_alerts, get_alert, list_silences), panel rendering (get_panel_image).
- Helm chart `mcp-observability-platform` with `Deployment` (with `checksum/config` rollout on ConfigMap changes), `Service`, `ServiceAccount`, `ClusterRole`+`Binding`, `ServiceMonitor`, `PodDisruptionBudget`, `NetworkPolicy` (ingress + configurable egress with auto-included kube-dns allow), `HorizontalPodAutoscaler` + `VerticalPodAutoscaler` (opt-in), and a runtime `ConfigMap` delivered to the pod via `envFrom` (timeouts, response cap, rate-limit thresholds, resolver TTL, OAuth refresh window). `values.schema.json`, helm-unittest specs under `tests/`, example overlays (memory, valkey, rbac-minimal, autoscaling), and a `README.md.gotmpl` for helm-docs. OAuth public client registration is `false` by default to match the server-side safe default.
- GitHub Actions CI workflow (`ci.yaml`) running Go test + vet, yamllint, Helm lint, helm-unittest, and govulncheck on every PR and push to main. Runs alongside the devctl-generated workflows (`zz_generated.*`) already in the repo.
- `Makefile.custom.mk` with CI-facing targets (`check`, `test-vet`, `helm-lint`, `helm-test`, `govulncheck`, `lint-yaml`) + developer conveniences (`tidy`, `helm-template`).
- Tool-handler middleware (`internal/server/middleware/`) applied globally via mcp-go's `WithToolHandlerMiddleware`: `Tracing()` (OTEL span per tool call) and `Metrics()` (counter + histogram). mcp-go's `WithRecovery()` is wired too — panic safety we did not have before. Tool registrations carry `tools.ReadOnlyAnnotation()` so `tools/list` advertises `readOnlyHint`, `openWorldHint`, `destructiveHint: false`.
- Deep readiness probes: `/readyz` now checks Grafana reachability (via `/api/health`), Dex OIDC discovery, and the K8s informer cache (2s per-check deadline). `/healthz/detailed` returns a JSON summary with per-check status, duration, uptime, and version for operators and dashboards.
- Structured audit trail (`internal/audit/` + `middleware.Audit()`): one JSON line per tool call on stderr with `{timestamp, caller, tool, args, outcome, duration_ms, error}`. Always on, stable schema, separate from the debug-gated diagnostic log so SIEM/compliance tooling can ingest without reverse-engineering. Outcome uses the same 3-bucket `ok`/`user_error`/`system_error` classification as metrics and spans so cross-signal correlation never drifts. A pluggable `Redactor` lets future tools that accept sensitive args (tokens, keys) mask them before the record is emitted.
- `MCP_TRANSPORT=stdio` and `MCP_TRANSPORT=sse` now actually serve their transports (previously accepted and rejected at startup). Stdio skips the HTTP + OAuth stack entirely and serves over stdin/stdout; SSE mounts `/sse` + `/message` under the same OAuth gate as streamable-http.
- `MCP_OAUTH_TRUSTED_AUDIENCES` (CSV): additional OAuth client IDs whose Dex-signed tokens are accepted as if minted for this server's own client ID. Enables SSO token-forwarding scenarios with muster / sibling MCPs. Same semantic as mcp-kubernetes. Empty = own-tokens-only.
- `MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES` (CSV): custom URI schemes accepted during public client registration beyond the always-allowed loopback HTTPS (e.g. `cursor,vscode`). Each scheme validated per RFC 3986 by mcp-oauth; web schemes (http/https) are allowed with a warning, dangerous schemes (javascript/data/file/ftp) are rejected.
- Helm chart values `oauth.trustedAudiences` and `oauth.trustedRedirectSchemes` (arrays of strings) plumb through the runtime ConfigMap to the new env vars; `values.schema.json` updated and helm-unittest coverage added.

### Changed

- `.circleci/config.yml` expanded from chart-only publish to the full `mcp-kubernetes` pattern: `architect/go-build`, multi-arch image push (amd64 on branches, multi-arch on tags to gsoci + all registries including China mirrors), and `run-tests-with-ats` for chart ATS tests.
- `renovate.json5` extended with the `lang-go.json5` preset.
- **Package restructure.** `internal/tracing/` merged into `internal/observability/`; caller identity plumbing moved to `internal/identity/`; tool handlers moved from `internal/server/tools_*.go` to `internal/tools/` (one file per category); tool middlewares live at `internal/server/middleware/`. MCP resources / prompts stubs stay in `internal/server/` as peer surfaces to tools.
- Tool-call `outcome` metric label expanded from `ok`/`err` to `ok`/`user_error`/`system_error` so operators can distinguish real incidents (5xx-class — Go error / panic) from expected user-visible failures (4xx-class — missing arg, authz denial, `response_too_large`). Spans carry the same classification as the `tool.outcome` attribute; span status is marked Error only on `system_error`.
- Go toolchain bumped to `1.25.5`; `mark3labs/mcp-go` to `v0.49.0` (ships `ToolHandlerMiddleware`, `WithRecovery`, `NewToolResultErrorf`).
- Two-phase graceful shutdown: drain the MCP server first (10s) while the observability server keeps answering liveness probes and Prometheus scrapes, then drain observability (5s). Prevents kubelet from killing the pod mid-tool-call because the liveness probe bounced while MCP was draining.
- `MCP_OAUTH_ENCRYPTION_KEY` now rejects keys with Shannon entropy below 4.0 bits/byte. Catches placeholder / all-zero / all-`a` inputs that pass mcp-oauth's length check but encrypt with a trivially-known key. Will propose upstream as a native `security.NewEncryptor` guard.
- Startup validates URL + OAuth client ID inputs via mcp-oauth's native exports (`oidc.ValidateHTTPSURL` on issuer URLs; `dex.ValidateAudience`/`ValidateAudiences` on client IDs and trusted-audience entries). Skipped when `MCP_OAUTH_ALLOW_INSECURE_HTTP=true` so local dev still works against an HTTP Dex.
- `internal/server.New` now returns `(*mcpsrv.MCPServer, error)` rather than `(http.Handler, error)`. HTTP-transport wrapping moved to new helpers `server.StreamableHTTPHandler(mcp)` and `server.SSEHandler(mcp)` so callers pick the transport. Stdio callers drive `mcpsrv.ServeStdio(mcp)` directly.

[Unreleased]: https://github.com/giantswarm/mcp-observability-platform/tree/main
