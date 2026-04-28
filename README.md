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
[`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) is the orientation
doc: request flow, package layout, threat model, and where to add a new
tool.

## MCP surface

### Tools

All tools require `role: viewer` on the target org (via the caller's OIDC
groups intersected with `GrafanaOrganization.spec.rbac`). Write operations
are intentionally out of scope for this MCP.

Most tool handlers are bridged to upstream
[`grafana/mcp-grafana`](https://github.com/grafana/mcp-grafana) — we add a
synthetic `org` argument and the registrar resolves it to the org's
OrgID + datasource UID before delegating. Categories without a usable
upstream equivalent (Tempo, Alertmanager v2 alerts, `list_orgs`) stay
local. See `internal/tools/doc.go` for the per-category rationale.

**Orgs & datasources**

| Tool               | Backend     | Notes                                                          |
| ------------------ | ----------- | -------------------------------------------------------------- |
| `list_orgs`        | (local CRs) | Minimal projection (name / displayName / orgID / role / tenantTypes) |
| `list_datasources` | Grafana API | `/api/datasources`; projected to id / uid / name / type        |
| `get_datasource`   | Grafana API | `/api/datasources/uid/{uid}` full detail                       |

**Dashboards**

| Tool                           | Backend     | Notes                                                          |
| ------------------------------ | ----------- | -------------------------------------------------------------- |
| `search_dashboards`            | Grafana API | `/api/search`, grouped by folder, page/pageSize over folders   |
| `search_folders`               | Grafana API | `/api/search?type=dash-folder`                                 |
| `get_dashboard_by_uid`         | Grafana API | Full dashboard JSON (usually 100s of KB — prefer the summary)  |
| `get_dashboard_summary`        | Grafana API | Title / tags / vars / row+panel tree (NO queries)              |
| `get_dashboard_panel_queries`  | Grafana API | Queries for one panel (by id or title substring) or all        |
| `get_dashboard_property`       | Grafana API | Sub-tree of the dashboard JSON by RFC 6901 JSON Pointer        |
| `generate_deeplink`            | Grafana URL | Builds `/d/{uid}?orgId=…&from=…&to=…&viewPanel=…&var-…`        |

**Metrics (Mimir)**

| Tool                                 | DS proxy path                                   |
| ------------------------------------ | ----------------------------------------------- |
| `query_prometheus`                   | `api/v1/query[_range]`                          |
| `query_prometheus_histogram`         | `histogram_quantile(...)` wrapper around `query_range` |
| `list_prometheus_metric_names`       | `api/v1/label/__name__/values`                  |
| `list_prometheus_label_names`        | `api/v1/labels`                                 |
| `list_prometheus_label_values`       | `api/v1/label/{label}/values`                   |
| `list_prometheus_metric_metadata`    | `api/v1/metadata`                               |
| `alerting_manage_rules`              | `api/v1/rules` — read-only meta-tool covering list / get / versions over Mimir-backed alert + recording rules |

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
| `list_tempo_tag_names`  | `api/v2/search/tags`                |
| `list_tempo_tag_values` | `api/v2/search/tag/{tag}/values`    |

**Alerts (Alertmanager)**

| Tool           | DS proxy path                                              |
| -------------- | ---------------------------------------------------------- |
| `list_alerts`  | `api/v2/alerts` — paged, severity-sorted                   |
| `get_alert`    | Single alert by fingerprint (derived from `list_alerts`)   |

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

Prometheus metrics served at `/metrics` on the merged HTTP port (default `:8080`):

| Metric                               | Type      | Labels           |
| ------------------------------------ | --------- | ---------------- |
| `mcp_tool_call_total`                | counter   | `tool`           |
| `mcp_tool_call_errors_total`         | counter   | `tool`           |
| `mcp_tool_call_duration_seconds`     | histogram | `tool`           |

Per-Grafana-request observation lives on the OTEL span emitted by
`internal/grafana.client.fetch` — no separate aggregate counter.

Plus default Go and process collectors.

Tool error rate is `errors_total / total` per tool — the standard two-counter pattern. Error spans are marked Error regardless of whether the handler returned a Go error or an `IsError` result.

### Tracing

OpenTelemetry tracing is wired via the standard `OTEL_EXPORTER_OTLP_*`
environment variables. When no endpoint is set, spans go to a no-op tracer
and the W3C trace-context propagator is still installed so incoming headers
are respected. Spans are emitted per tool call and per Grafana HTTP request.

`Instrument` middleware also writes a structured `tool_call` slog line
to the app logger (caller, tool, error, duration, trace_id, span_id) so
no-OTLP setups still get a queryable record. The cluster log pipeline
ships stderr to Loki; an MCP gateway can correlate via the trace IDs.

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
| `OAUTH_DEX_ISSUER_URL`                      | yes            | Dex issuer (read by `oauthconfig.DexFromEnv`)            |
| `OAUTH_DEX_CLIENT_ID`                       | yes            | Dex OAuth client                                         |
| `OAUTH_DEX_CLIENT_SECRET`                   | yes            | Dex OAuth client secret. `*_FILE` variant supported.     |
| `OAUTH_DEX_REDIRECT_URL`                    | no             | Provider callback URL. Defaults to `$OAUTH_ISSUER/oauth/callback`; only set if you need a non-canonical path. |
| `OAUTH_ISSUER`                              | yes            | Public issuer URL of this MCP                            |
| `OAUTH_ALLOW_INSECURE_HTTP`                 | no             | `true` to allow plain-HTTP OAuth flows (local dev only). Loopback issuers (`http://localhost`, `http://127.0.0.1`, `http://[::1]`) are accepted without this flag per RFC 8252. |
| `OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION`    | no             | `true` to open `/oauth/register` to unauthenticated callers (default `false`). Required for MCP CLI clients (Claude Code, mcp-inspector) that self-register at runtime. |
| `OAUTH_ALLOW_LOCALHOST_REDIRECT_URIS`       | no             | `true` to accept loopback redirect URIs (RFC 8252) at registration. The Helm chart defaults to `true` because every MCP CLI client (Claude Code, mcp-inspector, IDE plugins) registers a loopback URI. |
| `OAUTH_ENCRYPTION_KEY`                      | no             | AES-256 key for token-at-rest encryption; 44-char base64 (`openssl rand -base64 32`) or 64-char hex (`openssl rand -hex 32`). `*_FILE` variant supported. |
| `OAUTH_TRUSTED_AUDIENCES`                   | no             | CSV of OAuth client IDs whose tokens are accepted as if minted for this server — enables SSO token forwarding from muster or sibling MCPs. Tokens must still be signed by `OAUTH_DEX_ISSUER_URL`. Empty = own-tokens-only. |
| `OAUTH_TRUSTED_REDIRECT_SCHEMES`            | no             | CSV of custom URI schemes accepted during public client registration (e.g. `cursor,vscode`). Loopback HTTPS is always allowed; `javascript`/`data`/`file`/`ftp` are rejected regardless. |
| `OAUTH_STORAGE_BACKEND`                     | no             | `memory` (default) or `valkey` (read by `oauthconfig.StorageFromEnvWithPrefix("OAUTH_")`) |
| `OAUTH_VALKEY_ADDR` / `_PASSWORD` / `_TLS`  | no             | Required when `OAUTH_STORAGE_BACKEND=valkey`. `OAUTH_VALKEY_PASSWORD` accepts the `*_FILE` variant. |
| `MCP_TRANSPORT`                             | no             | `streamable-http` (default), `sse`, or `stdio`. Stdio has no HTTP surface and bypasses OAuth — developer-loop only. |
| `MCP_ADDR`                                  | no             | Listen address for the merged HTTP surface — `/mcp`, `/oauth/*`, `/metrics`, `/healthz`, `/readyz` are all on the same port (default `:8080`). Ignored when `MCP_TRANSPORT=stdio`. |
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

Runtime tunables (tool timeout, response cap, OAuth trust config) live
under `runtime:` / `oauth:` in `values.yaml` and are delivered through a
ConfigMap mounted via `envFrom`. A `checksum/config` annotation on the
pod template rolls the deployment whenever those change.

## Run locally (without Kubernetes)

```sh
go mod tidy
make build
./mcp-observability-platform serve
```

## Layout

See [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) for the package
layout table and what each package is responsible for.

## Related

- `giantswarm/mcp-oauth` — OAuth resource-server library used here
- `giantswarm/observability-operator` — owns the GrafanaOrganization CRD
- `giantswarm/mcp-prometheus`, `giantswarm/mcp-kubernetes` — sibling MCPs
  that set the conventions followed here
