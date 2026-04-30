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

Delegated (upstream, bind via `gfBinder`):

- `alerting_manage_routing` (read mode) — answers "where will this
  alert notify?". Sibling of `alerting_manage_rules`. Bound against
  prometheus-typed datasources (Mimir).
- `run_panel_query` — execute saved panel queries with template-var
  substitution. Org-scoped. Pairs with `get_dashboard_panel_queries`.
- `get_query_examples` — PromQL/LogQL syntax helper. Org-scoped only
  because `gfBinder` is the path of least resistance.
- `get_panel_image` (optional, off by default) — returns proper MCP
  `ImageContent` (base64 PNG); vision-capable LLM clients render it
  natively. Requires the Grafana Image Renderer service alongside
  Grafana. Ship disabled in the chart's `runtime.disabledTools` so
  clusters opt in only after the renderer is deployed.

Custom (no upstream equivalent in v0.13.0):

- `list_silences(org, state?, matcher?)` and `get_silence(org, id)` —
  Alertmanager v2 `/api/v2/silences{,/{id}}`. Today `list_alerts`
  returns `silencedBy: [<id>]` fingerprints that have no resolver.
  Same custom pattern as `list_alerts` / `get_alert`.

Depends on §4 (per-tool enable/disable) for `get_panel_image` to ship
disabled by default.

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

### 2. Cache `ListDatasources` per OrgID

The alerting fanout and every single-DS tool (after the live-fetch
migration) call `grafana.Client.ListDatasources` per request — one
extra Grafana RTT each. A small per-OrgID TTL cache (~30s) inside
`grafana.Client` would amortise this without invalidation complexity.

- Wrap `ListDatasources` with a `sync.Map`-backed cache keyed by
  OrgID; entries carry a deadline. Cache misses fetch and store; hits
  return the slice unchanged.
- TTL stays short enough that a freshly added/removed datasource
  surfaces within ~30s — matches the cadence of operator-driven
  datasource provisioning.
- `LookupDatasourceByUID` reuses the cached list (already does — it
  iterates `ListDatasources` output).

No operator-side change required. The wider §2-migration (live-fetch
for every single-DS tool, deleting `MatchKind` /
`LookupDatasourceUIDByID` / `Organization.Datasources`) shipped with
the optional `datasourceUid` override.

### 3. Write tools gated on Editor / Admin

The authz model (`Role` with Editor/Admin,
`GrafanaOrganization.spec.rbac.editors/admins`) supports this;
`Instrument` middleware already audits payloads. Highest-value
writes that match upstream:

- `create_silence(org, matchers, duration, comment)` — first slice,
  Editor-gated. Custom (no upstream equivalent), AM v2
  `POST /api/v2/silences`. Depends on §4 landing first so the tool
  ships in the chart's `runtime.disabledTools` and clusters opt in.
- `create_annotation(org, dashboardUid?, text, tags[])` — bot-driven
  deploy annotations. Delegated (`mcpgrafanatools.CreateAnnotationTool`).
  Land after `create_silence` validates the audit + authz path.
- `update_annotation(org, id, ...)` — partial-update. Delegated.

Each carries `destructiveHint: true` in MCP annotations; the
`tool_call` audit line captures full payload for forensics.

### 4. Per-tool enable/disable (deployment-time)

Operators should be able to disable individual tools (e.g. all write
tools in a read-only deployment, `alerting_manage_rules` in clusters
where alert-rule access is sensitive) without rebuilding.

- `TOOLS_DISABLED` env var (CSV) read at startup.
- `RegisterAll` skips `s.AddTool` for matching names and logs the
  skip set once at startup.
- Helm chart exposes a `runtime.disabledTools: []string` value.

### 5. Self-observability of the MCP itself

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
