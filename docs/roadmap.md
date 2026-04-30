# Roadmap

Forward-looking work plan. For what's already shipped:

- [`README.md`](../README.md) — architecture layout, MCP surface, configuration, metrics.
- [`CHANGELOG.md`](../CHANGELOG.md) — per-release feature list.
- `git log` — authoritative history.
- [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md) — request flow, package layout, threat model.

## Post-v0.1.0 priorities

### 0. Tool surface completion

Gaps from `mcp-grafana@v0.13.0` that fit the read-only scope but are
not bound today, plus one custom AM v2 surface upstream does not
cover.

Landed:

- `run_panel_query` — delegated (`mcpgrafanatools.RunPanelQuery`).
  Bound org-only; the upstream handler resolves its target datasource
  from the dashboard JSON. Pairs with `get_dashboard_panel_queries`.
- `get_query_examples` — delegated. Org-binding is for surface
  uniformity only; the upstream handler is Grafana-independent (it
  returns canned PromQL / LogQL / SQL strings).
- `list_silences(org, state?, matcher?)` and `get_silence(org, id)` —
  custom AM v2 `/api/v2/silences` and `/api/v2/silence/{id}` via
  Grafana's datasource proxy. Same pattern as `list_alerts` /
  `get_alert`: live-fetch picks the alertmanager-typed datasource
  (optional `datasourceUid` overrides). State narrowing
  (`active|pending|expired|all`, default `active`) is applied
  client-side because AM v2 only honours a label-matcher `filter`
  server-side. Resolves the `silencedBy` ids `list_alerts` surfaces.
- `get_panel_image` — delegated. Hits Grafana's `/render` endpoint
  and returns an MCP `ImageContent`. Always-on: clusters without the
  Grafana Image Renderer surface a clear "image renderer not
  available" error from upstream, same failure shape as any tool
  called on a missing backend.

Deferred:

- `alerting_manage_routing` — upstream tool only covers
  Grafana-managed routing for most operations
  (`get_notification_policies`, `get_contact_point`,
  `get_time_intervals`, `get_time_interval` all hit Grafana's
  Provisioning API; only `get_contact_points` accepts a
  `datasource_uid`). For Mimir-AM-backed orgs, the route tree, mute
  timings, and most contact-point details stay invisible — binding
  it as-is would mislead the LLM into thinking it has answered "where
  will this alert notify?" when it has not. Belongs upstream so every
  `mcp-grafana` user benefits, not just us. File the upstream issue
  and revisit once landed.

### 1. Per-org Grafana SA tokens (Phase-2 blast-radius fix)

Biggest unresolved security gap. Today one server-admin SA per MCP
pod exposes every Grafana org on compromise.

- `observability-operator` provisions per-`GrafanaOrganization` SAs
  into namespaced Secrets.
- MCP authorizer picks the right SA per-request based on
  `Organization.OrgID`.
- `GRAFANA_SA_TOKEN` / `GRAFANA_BASIC_AUTH` remain as bootstrap
  fallback, documented "dev/bootstrap only; never production".

Needs `observability-operator` coordination — open an issue there
describing the contract so the dependency is visible from both sides.

### 2. Write tools gated on Editor / Admin

The authz model (`Role` with Editor/Admin,
`GrafanaOrganization.spec.rbac.editors/admins`) supports this;
`Instrument` middleware already audits payloads. Highest-value
writes that match upstream:

- `create_silence(org, matchers, duration, comment)` — first slice,
  Editor-gated. Custom (no upstream equivalent), AM v2
  `POST /api/v2/silences`. Ship listed in the chart's `tools.disabled`
  default so clusters opt in explicitly via `--disabled-tools`.
- `create_annotation(org, dashboardUid?, text, tags[])` — bot-driven
  deploy annotations. Delegated (`mcpgrafanatools.CreateAnnotationTool`).
  Land after `create_silence` validates the audit + authz path.
- `update_annotation(org, id, ...)` — partial-update. Delegated.

Each carries `destructiveHint: true` in MCP annotations; the
`tool_call` audit line captures full payload for forensics.

### 3. Self-observability of the MCP itself

Shipped today: per-tool counter + duration histogram + error counter
on `/metrics` (`internal/observability/`), OTEL tracing, ServiceMonitor
template. Missing: a shipped Grafana dashboard and a shipped
`PrometheusRule`. Operators run the MCP without alerting on its own
auth failures, tool error rate, or tool-call latency p99 — a black box
in production.

Deliverables:

- `helm/mcp-observability-platform/templates/grafanadashboard.yaml` —
  sidecar-style ConfigMap with `grafana_dashboard: "1"` label. Panels:
  request rate (per tool), error rate (per tool), tool-call latency
  p50/p95/p99 (per tool), OAuth token-validation latency p99,
  authz-denial rate (per role), `tools/list` 5xx rate, in-flight
  request gauge.
- `helm/mcp-observability-platform/templates/prometheusrule.yaml` —
  PrometheusRule covering: sustained auth-failure rate (warn at >5%
  for 10m), per-tool error rate (warn at >5% for 10m), tool-call
  duration p99 SLO breach (warn at >5s for 15m), `/healthz` flapping
  (page on 3 transitions in 5m).
- Chart values: `dashboard.enabled` and `alerts.enabled`, both
  default `false` so operators opt in only when the Grafana sidecar
  / Prometheus operator are present.

Chart-side only; no Go changes needed.

## Out of scope (explicitly not doing)

- **Multi-cluster / federating MCP.** One MCP per Grafana is a design
  constraint.
- **Generic (non-Grafana) Prometheus/Loki/Tempo clients.** The
  current model leverages Grafana's tenant-header injection via its
  datasource proxy.
- **Custom error-envelope format.** MCP's `IsError: true` + plain
  text is what LLMs are trained on.
- **Result caching beyond the resolver TTL.** Invalidation
  complexity outweighs the savings.
- **Upstream feature parity** with Pyroscope / OnCall / Incident /
  Sift / Asserts in `grafana/mcp-grafana`. Keep the surface focused
  on Giant Swarm's stack.
- **Pyroscope / trace→profile correlation.** Not exposed; revisit if
  Giant Swarm adopts a profiling backend in-cluster. Until then,
  `mcpgrafana/tools/pyroscope.go` stays unbound.
- **Grafana OnCall.** Different product from Unified Alerting;
  upstream OnCall tools (`tools/oncall.go`) target the
  `grafana-irm-app` plugin via `grafana/amixr-api-go-client`. Out of
  scope unless GS deploys Grafana OnCall.
