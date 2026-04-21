# mcp-observability-platform

[![CircleCI](https://dl.circleci.com/status-badge/img/gh/giantswarm/mcp-observability-platform/tree/main.svg?style=svg)](https://dl.circleci.com/status-badge/redirect/gh/giantswarm/mcp-observability-platform/tree/main)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/giantswarm/mcp-observability-platform/badge)](https://securityscorecards.dev/viewer/?uri=github.com/giantswarm/mcp-observability-platform)

Giant Swarm's observability-platform MCP server. Exposes Grafana (plus its
Mimir / Loki / Tempo / Alertmanager datasources) to MCP clients, with
per-caller tenant and role scoping derived from `GrafanaOrganization` CRs.

One MCP per Grafana instance. Authentication via MCP OAuth (Dex as IdP).
Authorization resolved from the caller's OIDC groups against
`GrafanaOrganization.spec.rbac.{admins,editors,viewers}`.

> **Branch status (scaffold)**: this README describes the target MCP surface.
> The current branch ports the Go scaffold only — no tools, resources, or
> prompts are registered yet, the Helm chart is not present, and `/readyz`
> is a liveness-equivalent stub. See [`docs/roadmap.md`](./docs/roadmap.md)
> for the PR breakdown:
> - Tools / resources / prompts → `port-tools` (PR #10)
> - Helm chart → `pr-2-helm-hardening` (PR #5)
> - Deep readiness + two-phase shutdown → `pr-4-readiness` (PR #7)
> - CI expansion → `pr-3-ci` (PR #6)

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
| `list_orgs`        | (local CRs) | Minimal projection (name/displayName/orgID/role/tenantTypes)   |
| `list_datasources` | Grafana API | `/api/datasources`; projected to id/uid/name/type              |

**Dashboards**

| Tool                           | Backend     | Notes                                                          |
| ------------------------------ | ----------- | -------------------------------------------------------------- |
| `list_dashboards`              | Grafana API | `/api/search`, grouped by folder, page/pageSize over folders   |
| `get_dashboard_summary`        | Grafana API | Dashboard JSON projected to title / tags / vars / row+panel tree (NO queries) |
| `get_dashboard_panel_queries`  | Grafana API | Queries for a single panel (by id or titleContains) or all    |
| `generate_deeplink`            | Grafana URL | Builds `/d/{uid}?orgId=…&from=…&to=…&viewPanel=…&var-…`        |
| `get_panel_image`              | Grafana     | Renders a panel as PNG via `/render/d-solo/{uid}` (see below) |

**Metrics (Mimir)**

| Tool                                 | DS proxy path                            |
| ------------------------------------ | ---------------------------------------- |
| `query_metrics`                      | `api/v1/query[_range]`                   |
| `list_prometheus_metric_names`       | `api/v1/label/__name__/values`           |
| `list_prometheus_label_names`        | `api/v1/labels`                          |
| `list_prometheus_label_values`       | `api/v1/label/{label}/values`            |
| `list_prometheus_metric_metadata`    | `api/v1/metadata`                        |
| `list_alert_rules`                   | `api/v1/rules` — filter by type/state/name |

**Logs (Loki)**

| Tool                       | DS proxy path                               |
| -------------------------- | ------------------------------------------- |
| `query_logs`               | `loki/api/v1/query_range` (returns `nextStart` cursor) |
| `list_loki_label_names`    | `loki/api/v1/labels`                        |
| `list_loki_label_values`   | `loki/api/v1/label/{label}/values`          |
| `query_loki_stats`         | `loki/api/v1/index/stats`                   |

**Traces (Tempo)**

| Tool                    | DS proxy path                       |
| ----------------------- | ----------------------------------- |
| `query_traces`          | `api/search`                        |
| `query_tempo_metrics`   | `api/metrics/query_range`           |
| `list_tempo_tag_names`  | `api/v2/search/tags`                |
| `list_tempo_tag_values` | `api/v2/search/tag/{tag}/values`    |

**Alerts (Alertmanager)**

| Tool         | DS proxy path                               |
| ------------ | ------------------------------------------- |
| `list_alerts` | `api/v2/alerts` — paged, severity-sorted   |

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

### Prompts

| Prompt                | Arguments                              | Purpose                                                |
| --------------------- | -------------------------------------- | ------------------------------------------------------ |
| `investigate-alert`   | `org`, `fingerprint`, `lookback?`      | Triage playbook: pulls alert detail then correlates logs / metrics / traces and produces a short report. Only read operations. |

### Resource templates

| URI template                                     | Description                                          |
| ------------------------------------------------ | ---------------------------------------------------- |
| `observability://org/{name}`                     | Per-org metadata + your role + tenants + datasources |
| `grafana://org/{name}/dashboard/{uid}`           | Full dashboard JSON                                  |
| `alertmanager://org/{name}/alert/{fingerprint}`  | Full Alertmanager alert object                       |

### Metrics

Prometheus metrics served at `:9091/metrics`:

| Metric                                                       | Type      | Labels             |
| ------------------------------------------------------------ | --------- | ------------------ |
| `mcp_observability_platform_tool_call_total`                 | counter   | `tool`, `outcome`  |
| `mcp_observability_platform_tool_call_duration_seconds`      | histogram | `tool`, `outcome`  |
| `mcp_observability_platform_grafana_proxy_total`             | counter   | `path`             |
| `mcp_observability_platform_grafana_proxy_duration_seconds`  | histogram | `path`             |
| `mcp_observability_platform_org_cache_size`                  | gauge     | —                  |

Plus default Go and process collectors.

### Tracing

OpenTelemetry tracing is wired via the standard `OTEL_EXPORTER_OTLP_*`
environment variables. When no endpoint is set, spans go to a no-op tracer
and the W3C trace-context propagator is still installed so incoming headers
are respected. Spans are emitted per tool call and per Grafana HTTP request.

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

## Configuration

Env-var driven. Flags override env. See `cmd/serve.go`.

| Env var                                     | Required       | Purpose                                                  |
| ------------------------------------------- | -------------- | -------------------------------------------------------- |
| `GRAFANA_URL`                               | yes            | Grafana base URL (in-cluster)                            |
| `GRAFANA_SA_TOKEN`                          | one-of         | Grafana **server-admin** SA token (see below)            |
| `GRAFANA_BASIC_AUTH`                        | one-of         | `user:password` for the built-in admin, as an alternative to `GRAFANA_SA_TOKEN` when SA promotion is unavailable |
| `GRAFANA_PUBLIC_URL`                        | no             | Human-facing Grafana URL for deeplinks; defaults to `GRAFANA_URL` |
| `DEX_ISSUER_URL`                            | yes            | Dex issuer                                               |
| `DEX_CLIENT_ID`                             | yes            | Dex OAuth client                                         |
| `DEX_CLIENT_SECRET`                         | yes            | Dex OAuth client secret                                  |
| `MCP_OAUTH_ISSUER`                          | yes            | Public issuer URL of this MCP                            |
| `MCP_OAUTH_REDIRECT_URL`                    | no             | Defaults to `$MCP_OAUTH_ISSUER/oauth/callback`           |
| `MCP_OAUTH_ALLOW_INSECURE_HTTP`             | no             | `true` to allow plain-HTTP OAuth flows (local dev only)  |
| `MCP_OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION`| no             | `true` to open `/oauth/register` (default `false`). Required for MCP CLI clients (Claude Code, mcp-inspector) that use loopback redirect URIs per RFC 8252. |
| `MCP_OAUTH_ENCRYPTION_KEY`                  | no             | AES-256 key for token encryption at rest; 64-char hex or 32 raw bytes |
| `OAUTH_STORAGE`                             | no             | `memory` (default) or `valkey`                           |
| `VALKEY_ADDR` / `_PASSWORD` / `_TLS`        | no             | Required when `OAUTH_STORAGE=valkey`                     |
| `MCP_TRANSPORT`                             | no             | `streamable-http` only (default). `sse` / `stdio` reserved for a later PR and currently rejected at startup. |
| `MCP_ADDR`                                  | no             | Listen address for the MCP transport (default `:8080`)   |
| `METRICS_ADDR`                              | no             | Listen address for `/metrics`, `/healthz`, `/readyz` (default `:9091`) |
| `TOOL_MAX_RESPONSE_BYTES`                   | no             | Cap on tool response body (default 131072; 0 = disabled) |
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

## Local install

Build and run the binary locally:

```sh
go mod tidy
make build
./mcp-observability-platform serve
```

Helm chart installation lands in PR #5 (`pr-2-helm-hardening`). Once that
chart is merged, the typical deploy will be:

```sh
helm install mcp-observability-platform ./helm/mcp-observability-platform \
  --namespace observability --create-namespace \
  --set grafana.url=https://grafana.example.com \
  --set oauth.issuer=https://mcp.example.com \
  --set oauth.dex.issuerUrl=https://dex.example.com \
  --set oauth.dex.clientId=mcp-observability-platform
```

With secrets created beforehand:

```sh
kubectl -n observability create secret generic mcp-observability-platform-grafana \
  --from-literal=serviceAccountToken=<token>

kubectl -n observability create secret generic mcp-observability-platform-oauth \
  --from-literal=clientSecret=<dex-client-secret>
```

## Layout

```
cmd/              Cobra CLI (serve, version)
internal/
  authz/          GrafanaOrganization CR cache + role resolver
  grafana/        Grafana HTTP client (server-admin SA + X-Grafana-Org-Id)
  server/         mark3labs/mcp-go wiring: tools + resource templates
helm/mcp-observability-platform/   Helm chart
```

## Related

- `giantswarm/mcp-oauth` — OAuth resource-server library used here
- `giantswarm/observability-operator` — owns the GrafanaOrganization CRD
- `giantswarm/mcp-prometheus`, `giantswarm/mcp-kubernetes` — sibling MCPs
  that set the conventions followed here
