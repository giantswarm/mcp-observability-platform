# mcp-observability-platform

[![CircleCI](https://dl.circleci.com/status-badge/img/gh/giantswarm/mcp-observability-platform/tree/main.svg?style=svg)](https://dl.circleci.com/status-badge/redirect/gh/giantswarm/mcp-observability-platform/tree/main)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/giantswarm/mcp-observability-platform/badge)](https://securityscorecards.dev/viewer/?uri=github.com/giantswarm/mcp-observability-platform)

Giant Swarm's observability-platform MCP server. Exposes Grafana (plus its
Mimir / Loki / Tempo / Alertmanager datasources) to MCP clients, with
per-caller tenant and role scoping derived from `GrafanaOrganization` CRs.

One MCP per Grafana instance. Authentication via MCP OAuth (Dex as IdP).
Authorization resolved from the caller's OIDC groups against
`GrafanaOrganization.spec.rbac.{admins,editors,viewers}`. Role and org-
membership changes propagate within ~30s (the resolver's positive cache
TTL); shorter TTLs for negative results (caller-not-yet-provisioned).

## Roadmap

See [`docs/roadmap.md`](./docs/roadmap.md) for the productionization plan
and [`docs/upstream-contributions.md`](./docs/upstream-contributions.md)
for the parallel `grafana/mcp-grafana` contribution lane.

## MCP surface

### Tools

All tools require `role: viewer` on the target org (via the caller's OIDC
groups intersected with `GrafanaOrganization.spec.rbac`). Write operations
are intentionally out of scope for this MCP.

**Orgs & datasources**

| Tool               | Backend     | Notes                                                          |
| ------------------ | ----------- | -------------------------------------------------------------- |
| `list_orgs`        | (local CRs) | Minimal projection (name / displayName / orgID / role / tenantTypes) |
| `list_datasources` | Grafana API | `/api/datasources`; projected to id / uid / name / type        |
| `get_datasource`   | Grafana API | `/api/datasources/uid/{uid}` full detail                       |

**Dashboards & annotations**

| Tool                           | Backend     | Notes                                                          |
| ------------------------------ | ----------- | -------------------------------------------------------------- |
| `search_dashboards`            | Grafana API | `/api/search`, grouped by folder, page/pageSize over folders   |
| `search_folders`               | Grafana API | `/api/search?type=dash-folder`                                 |
| `get_dashboard_by_uid`         | Grafana API | Full dashboard JSON (usually 100s of KB — prefer the summary)  |
| `get_dashboard_summary`        | Grafana API | Title / tags / vars / row+panel tree (NO queries)              |
| `get_dashboard_panel_queries`  | Grafana API | Queries for one panel (by id or title substring) or all        |
| `get_dashboard_property`       | Grafana API | Sub-tree of the dashboard JSON by RFC 6901 JSON Pointer        |
| `get_annotations`              | Grafana API | `/api/annotations` filtered by time / dashboard / panel / tags |
| `get_annotation_tags`          | Grafana API | `/api/annotations/tags` for annotation-tag discovery           |
| `generate_deeplink`            | Grafana URL | Builds `/d/{uid}?orgId=…&from=…&to=…&viewPanel=…&var-…`        |
| `get_panel_image`              | Grafana     | Renders a panel as PNG via `/render/d-solo/{uid}` (see below)  |
| `run_panel_query`              | Grafana API | Runs a panel's stored query directly (auto-routes Mimir/Loki/Tempo) |

**Metrics (Mimir)**

| Tool                                 | DS proxy path                                   |
| ------------------------------------ | ----------------------------------------------- |
| `query_prometheus`                   | `api/v1/query[_range]`                          |
| `query_prometheus_histogram`         | `histogram_quantile(...)` wrapper around `query_range` |
| `list_prometheus_metric_names`       | `api/v1/label/__name__/values`                  |
| `list_prometheus_label_names`        | `api/v1/labels`                                 |
| `list_prometheus_label_values`       | `api/v1/label/{label}/values`                   |
| `list_prometheus_metric_metadata`    | `api/v1/metadata`                               |
| `list_alert_rules`                   | `api/v1/rules` — filter by type / state / name  |
| `get_alert_rule`                     | `api/v1/rules` — single rule by name + group    |

**Logs (Loki)**

| Tool                       | DS proxy path                                            |
| -------------------------- | -------------------------------------------------------- |
| `query_loki_logs`          | `loki/api/v1/query_range` (returns `nextStart` cursor)   |
| `query_loki_patterns`      | `loki/api/v1/patterns` — log-pattern detection           |
| `query_loki_stats`         | `loki/api/v1/index/stats`                                |
| `list_loki_label_names`    | `loki/api/v1/labels`                                     |
| `list_loki_label_values`   | `loki/api/v1/label/{label}/values`                       |

**Traces (Tempo)**

| Tool                    | DS proxy path                       |
| ----------------------- | ----------------------------------- |
| `query_traces`          | `api/search`                        |
| `query_tempo_metrics`   | `api/metrics/query_range`           |
| `list_tempo_tag_names`  | `api/v2/search/tags`                |
| `list_tempo_tag_values` | `api/v2/search/tag/{tag}/values`    |

**Alerts & silences (Alertmanager)**

| Tool           | DS proxy path                                              |
| -------------- | ---------------------------------------------------------- |
| `list_alerts`  | `api/v2/alerts` — paged, severity-sorted                   |
| `get_alert`    | Single alert by fingerprint (derived from `list_alerts`)   |
| `list_silences` | `api/v2/silences` — paged, end-time-sorted                |

**Triage co-pilots**

Composite tools that synthesise common SRE-triage queries from a few inputs.
Names mirror grafana/mcp-grafana's Sift surface where an upstream equivalent
exists; OSS-only — no Grafana Cloud Sift backend required.

| Tool                       | Composes                                                                         |
| -------------------------- | -------------------------------------------------------------------------------- |
| `find_error_pattern_logs`  | service-label probe → `loki/api/v1/index/stats` size guard → `query_range` with error-keyword regex |
| `find_slow_requests`       | TraceQL builder (`resource.service.name` + `duration > min` + optional `status=error`) → Tempo `api/search` |
| `explain_query`            | wraps PromQL in `count(...)` → Mimir `api/v1/query` for series-count preflight   |

### Resources and prompts — deliberately not implemented

LLMs handle tool calls far more reliably than resource URIs or prompts, so
this MCP exposes only tools. A prior prototype had `observability://` /
`grafana://` / `alertmanager://` resource URIs and an `investigate-alert`
prompt; both were removed in favour of the equivalent tools (`get_alert`,
`get_dashboard_by_uid`, `list_datasources`). See
[`docs/roadmap.md`](./docs/roadmap.md#out-of-scope) for the rationale.

Datasource selection is per-org: tools match datasources from
`status.dataSources[]` by name substring (`mimir`, `loki`, `tempo`,
`alertmanager`). The tenant header is already baked into the datasource JSON
by observability-operator, so the MCP only picks the right datasource and
lets Grafana apply the header.

Caller identity is propagated to Grafana via `X-Grafana-User` on every
downstream request so Grafana's audit log attributes to the OIDC subject
rather than the server-admin SA.

### Response-size discipline

Tool responses that would exceed `TOOL_MAX_RESPONSE_BYTES` (default 128 KiB)
return a structured error payload:

```json
{
  "error": "response_too_large",
  "bytes": 245760,
  "limit": 131072,
  "message": "response is 245760 bytes, exceeds 131072 byte limit",
  "hint": "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range"
}
```

LLM clients can react programmatically instead of silently truncating.

For endpoints where pagination is natural (logs, label-values, rule lists,
tag values, dashboards-by-folder, alerts) tools expose `page`/`pageSize` or
a `nextStart` cursor so callers can page forward without re-running the
whole query.

### Metrics

Prometheus metrics served at `:9091/metrics`:

| Metric                               | Type      | Labels            |
| ------------------------------------ | --------- | ----------------- |
| `mcp_tool_call_total`                | counter   | `tool`, `outcome` |
| `mcp_tool_call_duration_seconds`     | histogram | `tool`, `outcome` |
| `mcp_grafana_proxy_total`            | counter   | `path`            |
| `mcp_grafana_proxy_duration_seconds` | histogram | `path`, `status`  |
| `mcp_org_cache_size`                 | gauge     | —                 |

Plus default Go and process collectors.

`outcome` values:

- `ok` — handler returned a non-error result.
- `user_error` — handler returned `isError: true` (user-visible failure such as a missing arg, authz denial, or `response_too_large`). Expected behaviour.
- `system_error` — handler returned a Go error (upstream unreachable, panic caught by mcp-go's `WithRecovery`, bug). Ops-actionable.

Spans mirror this on the `tool.outcome` attribute; the span is marked Error only on `system_error` (user errors are normal, same convention as HTTP servers not marking 4xx Error).

### Tracing and logs

OpenTelemetry tracing is wired via the standard `OTEL_EXPORTER_OTLP_*`
environment variables. When no endpoint is set, spans go to a no-op tracer
and the W3C trace-context propagator is still installed so incoming headers
are respected. Spans are emitted per tool call and per Grafana HTTP request.

When `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` (or the shared
`OTEL_EXPORTER_OTLP_ENDPOINT`) is set, slog records — operator logs and
the audit stream — fan out through the `otelslog` bridge, so every record
carries `trace_id` + `span_id` from ctx. Operators can click from a
tool-call span straight to the surrounding log lines in Loki/Grafana
without a correlation-ID scheme.

### Image renderer (for `get_panel_image`)

`get_panel_image` proxies to Grafana's `/render/d-solo/{uid}` endpoint,
which requires the `grafana-image-renderer` plugin. Without it, Grafana
returns HTML and the tool responds with an actionable error pointing at
this setup. Two deployment options:

1. **Plugin installed in Grafana** — set `GF_INSTALL_PLUGINS=grafana-image-renderer`
   in the Grafana container. Simple, but Chromium adds ~300 MB and CPU/mem
   pressure on the Grafana pod.
2. **Standalone renderer service (recommended)** — deploy
   `grafana/grafana-image-renderer` as its own Deployment/Service in the
   cluster, and set on Grafana:
   - `GF_RENDERING_SERVER_URL=http://grafana-image-renderer:8081/render`
   - `GF_RENDERING_CALLBACK_URL=http://grafana:3000/`

Either way, no changes are required on this MCP once the renderer is
reachable from Grafana.

### Transports

Three MCP transports are wired, selected via `MCP_TRANSPORT`:

| Transport | When to use | OAuth | Listens on |
| --- | --- | --- | --- |
| `streamable-http` (default) | Remote deployment gated by OAuth, the shipping Helm-chart deployment. Works with `claude mcp add --transport http …`, mcp-inspector, browser clients. | Required (mcp-oauth + Dex) | `$MCP_ADDR` (default `:8080`), `POST /mcp` |
| `sse` | Remote deployment for MCP clients that still prefer SSE (`text/event-stream`). Identical auth and tool surface as streamable-http. | Required | `$MCP_ADDR`, `GET /sse` + `POST /messages` |
| `stdio` | Local-dev and desktop-client integrations (Claude Desktop's `command` server entry, IDE plugins). No HTTP listener, no OAuth — the client is whoever spawned the process, so authz relies on the caller already having the right Grafana / Kubernetes context. | None | stdin/stdout |

OAuth is only meaningful for the network transports; `stdio` treats the
spawning process as fully trusted (same model as `kubectl` delegating to the
user's kubeconfig). Configuration env vars are shared across transports —
the Grafana / Dex / OAuth / observability settings apply identically.

## Configuration

Env-var driven. Flags override env. See `cmd/serve.go`.

| Env var                                     | Required       | Purpose                                                  |
| ------------------------------------------- | -------------- | -------------------------------------------------------- |
| `GRAFANA_URL`                               | yes            | Grafana base URL (in-cluster)                            |
| `GRAFANA_SA_TOKEN`                          | one-of         | Grafana **server-admin** SA token (see below). Production path. |
| `GRAFANA_BASIC_AUTH`                        | one-of         | `user:password` for the built-in admin — dev/bootstrap only when SA promotion is unavailable. Setting both `GRAFANA_SA_TOKEN` and this var is a startup error. |
| `GRAFANA_PUBLIC_URL`                        | no             | Human-facing Grafana URL for deeplinks; defaults to `GRAFANA_URL` |
| `DEX_ISSUER_URL`                            | yes            | Dex issuer                                               |
| `DEX_CLIENT_ID`                             | yes            | Dex OAuth client                                         |
| `DEX_CLIENT_SECRET`                         | yes            | Dex OAuth client secret                                  |
| `OAUTH_ISSUER`                              | yes            | Public issuer URL of this MCP                            |
| `OAUTH_REDIRECT_URL`                        | no             | Defaults to `$OAUTH_ISSUER/oauth/callback`               |
| `OAUTH_ALLOW_INSECURE_HTTP`                 | no             | `true` to allow plain-HTTP OAuth flows (local dev only)  |
| `OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION`    | no             | `true` to open `/oauth/register` (default `false`). Required for MCP CLI clients (Claude Code, mcp-inspector) that use loopback redirect URIs per RFC 8252. |
| `OAUTH_ENCRYPTION_KEY`                      | no             | AES-256 key for token encryption at rest; 64-char hex (`openssl rand -hex 32`) or 44-char standard base64 (`openssl rand -base64 32`). Rejected if entropy is too low (all-zeros, repeated byte). |
| `OAUTH_TRUSTED_AUDIENCES`                   | no             | CSV of OAuth client IDs whose tokens are accepted as if minted for this server — enables SSO token forwarding from muster or sibling MCPs. Tokens must still be signed by `DEX_ISSUER_URL`. Empty = own-tokens-only. |
| `OAUTH_TRUSTED_REDIRECT_SCHEMES`            | no             | CSV of custom URI schemes accepted during public client registration (e.g. `cursor,vscode`). Loopback HTTPS is always allowed; `javascript`/`data`/`file`/`ftp` are rejected regardless. |
| `OAUTH_STORAGE`                             | no             | `memory` (default) or `valkey`                           |
| `VALKEY_ADDR` / `_PASSWORD` / `_TLS`        | no             | Required when `OAUTH_STORAGE=valkey`                     |
| `MCP_TRANSPORT`                             | no             | `streamable-http` (default), `sse`, or `stdio`. Stdio has no HTTP surface and bypasses OAuth — developer-loop only. |
| `MCP_ADDR`                                  | no             | Listen address for the MCP transport (default `:8080`). Ignored when `MCP_TRANSPORT=stdio`. |
| `METRICS_ADDR`                              | no             | Listen address for `/metrics`, `/healthz`, `/readyz`, `/healthz/detailed` (default `:9091`) |
| `TOOL_MAX_RESPONSE_BYTES`                   | no             | Cap on tool response body (default 131072; 0 = disabled) |
| `TOOL_TIMEOUT`                              | no             | Per-tool-call deadline (default `30s`; `0` = disabled). Go duration syntax (`500ms`, `2m`). A tool exceeding the deadline returns an IsError result with timeout text. |
| `OTEL_EXPORTER_OTLP_ENDPOINT`               | no             | OTLP endpoint for span export; spans are no-op when unset |
| `OTEL_EXPORTER_OTLP_PROTOCOL`               | no             | `http/protobuf` (default) or `grpc`                      |
| `POD_NAME` / `POD_NAMESPACE` / `NODE_NAME`  | no             | Downward-API attributes added to OTEL resource when set  |
| `DEBUG`                                     | no             | `true` to enable debug logging                           |

### Grafana service-account token

Phase 1 uses a single **server-admin** Grafana service account. In Grafana:

1. Log in as a server admin.
2. Go to **Administration → General → Service accounts → Add**.
3. Assign the **Grafana Admin** *server* role. (Not the org-level Admin — that
   would make the SA org-scoped and `X-Grafana-Org-Id` would be ignored.)
4. Generate a token.
5. Put it in a Kubernetes Secret under the key `serviceAccountToken` and
   reference that secret via `grafana.existingSecret` in `values.yaml`.

The MCP performs a startup self-check calling `GET /api/orgs`; if the token
is not server-admin it fails to start.

**Known phase-1 blast-radius limitation**: one compromised MCP pod exposes
every Grafana org. Phase 2 narrows this by switching to per-org SA tokens
provisioned by the observability-operator (tracked in the plan).

## Install

Create the required secrets first. The `serviceAccountToken` key holds the
**Grafana** server-admin service-account token (not a Kubernetes one — the
chart creates the K8s ServiceAccount itself via `templates/serviceaccount.yaml`).

```sh
kubectl -n observability create secret generic mcp-observability-platform-grafana \
  --from-literal=serviceAccountToken=<grafana-sa-token>

kubectl -n observability create secret generic mcp-observability-platform-oauth \
  --from-literal=clientSecret=<dex-client-secret>
```

Then install the chart:

```sh
helm install mcp-observability-platform ./helm/mcp-observability-platform \
  --namespace observability --create-namespace \
  --set grafana.url=https://grafana.example.com \
  --set oauth.issuer=https://mcp-observability-platform.example.com \
  --set oauth.dex.issuerUrl=https://dex.example.com \
  --set oauth.dex.clientId=mcp-observability-platform
```

Example overlays live under `helm/mcp-observability-platform/values-*.yaml`:

| Values file                | Scenario                                                         |
| -------------------------- | ---------------------------------------------------------------- |
| `values-memory.yaml`       | Dev/test: memory-backed OAuth store, debug logging.              |
| `values-valkey.yaml`       | Prod: Valkey-backed OAuth store (durable, shared across replicas). |
| `values-rbac-minimal.yaml` | Externally-managed ServiceAccount + ClusterRoleBinding.          |
| `values-autoscaling.yaml`  | HPA + VPA (Initial) + PDB + NetworkPolicy (ingress + egress).    |

Runtime tunables (timeouts, response cap, rate-limit thresholds, OAuth
refresh window) live under `runtime:` in `values.yaml` and are delivered
through a ConfigMap mounted via `envFrom`. A `checksum/config` annotation
on the pod template rolls the deployment whenever those change.

## Run locally (without Kubernetes)

```sh
go mod tidy
make build
./mcp-observability-platform serve
```

## Layout

```
cmd/                               Cobra CLI (serve, version)
internal/
  authz/                           GrafanaOrganization CR cache + role resolver
  grafana/                         Grafana HTTP client (server-admin SA + X-Grafana-Org-Id)
  identity/                        Caller identity plumbing (OIDC UserInfo on ctx)
  observability/                   Prometheus metrics + OTEL tracing setup
  server/                          mcp-go transport wiring
    middleware/                    Tracing + Metrics runtime interceptors
  tools/                           MCP tool surface (32 tools) + shared tool helpers
helm/mcp-observability-platform/   Helm chart
  templates/                       Deployment, Service, ClusterRole(Binding), ConfigMap, NetworkPolicy, HPA, VPA, PDB, ServiceMonitor, NOTES
  tests/                           helm-unittest specs (configmap, deployment, hpa, networkpolicy, pdb, servicemonitor, vpa)
  values-{memory,valkey,rbac-minimal,autoscaling}.yaml   Opinionated overlays
```

## Related

- `giantswarm/mcp-oauth` — OAuth resource-server library used here
- `giantswarm/observability-operator` — owns the GrafanaOrganization CRD
- `giantswarm/mcp-prometheus`, `giantswarm/mcp-kubernetes` — sibling MCPs
  that set the conventions followed here
