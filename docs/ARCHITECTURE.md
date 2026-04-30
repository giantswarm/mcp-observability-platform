# Architecture

One-page reference for contributors. Forward-looking work plan lives in
[`roadmap.md`](./roadmap.md); the per-tool surface and configuration
reference are in [`README.md`](../README.md); per-release feature list is
in [`CHANGELOG.md`](../CHANGELOG.md); `git log` is the authoritative
history. This file is for orientation: where things live, what the
security boundaries are, and where to add a new tool.

## Request flow

```
                     ┌────────────────────────────────────────────────────────┐
                     │ MCP client (Claude Code, mcp-inspector, IDE plugins)    │
                     └──────────────────────────┬─────────────────────────────┘
                                                │ HTTP + Bearer (or stdio)
                                                ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ HTTP transport (cmd/mux.go)                                    │
              │   • streamable-http (default) at /mcp                          │
              │   • SSE at /sse + /message                                     │
              │   • stdio (no HTTP, no OAuth) for local dev                    │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ mcp-oauth ValidateToken                                        │
              │   • verifies bearer signed by OAUTH_DEX_ISSUER_URL                   │
              │   • or by an OAUTH_TRUSTED_AUDIENCES client (SSO forwarding)   │
              │   • puts UserInfo on request context                           │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ authz.PromoteOAuthCaller                                       │
              │   lifts UserInfo → MCP handler context as authz.Caller         │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ MCP server (internal/server) — middleware stack:               │
              │   1. WithRecovery       (mcp-go panic guard)                   │
              │   2. Instrument         span + counter/histogram + tool_call   │
              │                         audit slog line                        │
              │   3. RequireCaller      fail-closed if no caller in ctx        │
              │   4. ResponseCap        replace oversized text with structured │
              │   5. ToolTimeout        per-handler context deadline           │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ Tool handler (internal/tools/*.go)                             │
              │                                                                │
              │ Delegated tools (most of the surface):                         │
              │   internal/tools/grafanabind.go gfBinder.{bindOrgTool,          │
              │   bindDatasourceTool, bindDatasourceFanoutTool}                │
              │     1. read "org" from CallToolRequest                         │
              │     2. az.RequireOrg(ctx, org, role) → Organization            │
              │     3. Datasource path: caller-supplied datasourceUid wins     │
              │        (validated via grafana.LookupDatasourceByUID); else     │
              │        first match from grafana.ListDatasources by plugin     │
              │        type, UID injected server-side                          │
              │     4. attach mcpgrafana.GrafanaConfig (OrgID, X-Grafana-User) │
              │     5. delegate to upstream grafana/mcp-grafana handler        │
              │                                                                │
              │ Local tools (Alertmanager v2) call az.RequireOrg then          │
              │ grafana.Client.DatasourceProxy directly. list_orgs uses        │
              │ az.ListOrgs (no datasource).                                   │
              │                                                                │
              │ Tempo tools delegate to Tempo's own MCP server                 │
              │ (internal/tools/tempo.go): registered via                      │
              │ gfBinder.bindDatasourceTool with a per-UID ProxiedClient cache │
              │ behind the handler. Endpoint:                                  │
              │ /api/datasources/proxy/uid/<uid>/api/mcp.                      │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ Grafana datasource proxy → Mimir / Loki / Tempo / Alertmanager │
              │   • X-Grafana-User attributes the call to the OIDC subject     │
              │   • X-Grafana-Org-Id selects the org                           │
              │   • observability-operator pre-baked tenant headers in the DS  │
              └────────────────────────────────────────────────────────────────┘
```

## Package layout

| Package | Responsibility |
|---|---|
| `cmd/` | Cobra CLI + composition root. `runServe` wires the dex provider, OAuth server, K8s informer cache, MCP server, and the two HTTP listeners (MCP on `MCP_ADDR`, observability on `METRICS_ADDR`) in deterministic phase order. OAuth/Dex/storage/encryption env loading is delegated to `mcp-oauth/oauthconfig.*FromEnv`; this package owns Grafana credentials, the K8s informer adapter, the readiness probes, and the orchestration. |
| `internal/server/` | MCP server construction; middleware composition (Instrument / RequireCaller / ResponseCap / ToolTimeout); transport wrappers for streamable-HTTP and SSE. |
| `internal/server/middleware/` | One file per middleware. `Instrument` emits the span + metrics + structured `tool_call` slog line in lockstep so the three signals never drift. |
| `internal/authz/` | Caller identity (`caller.go`), role enum (`role.go`), org-access types, `Authorizer` interface (`authorizer.go`) + per-caller TTL cache. `OrgLister` is a domain port — the K8s informer adapter sits in `cmd/orglister.go`. |
| `internal/grafana/` | HTTP client (`VerifyServerAdmin`, `LookupUser`, `UserOrgs`, `ListDatasources`, `LookupDatasourceByUID`, `DatasourceProxy`) plus `Datasource` and `DatasourceType` (`MatchesType` / `FilterDatasourcesByType`). Delegated tools talk to upstream's `mcpgrafana.GrafanaClient` instead of this client. `RequestOpts{OrgID, Caller}` is set per call; `validateDatasourceProxyPath` guards against traversal. `ListDatasources` is cached per-OrgID (30s TTL); `LookupDatasourceByUID` inherits transparently. |
| `internal/tools/` | One file per tool category. Delegated to upstream `grafana/mcp-grafana`: `dashboards.go`, `metrics.go`, `logs.go`, `alerting.go`, `examples.go` (plus the delegated datasource tools in `orgs.go`). Delegated to Tempo's own MCP server (`/api/mcp`) via `mcp-grafana`'s `ProxiedClient`, registered through the same `gfBinder.bindDatasourceTool` path: `tempo.go`. Local: `orgs.go` (`list_orgs`), `alerts.go`, `silences.go`. Shared: `datasource.go`, `pagination.go`, `tools.go`, `grafanabind.go`. The unexported `gfBinder` wires upstream handlers onto our MCP server — `bindOrgTool` for org-only tools, `bindDatasourceTool` for datasource-scoped tools (resolves the org's datasource UID and injects it server-side so the LLM keeps the simple `{org, …}` shape). Every `s.AddTool` call site goes through `maybeAddTool`, which honours the `--disabled-tools` filter wired in from `cmd/serve.go` so operators can drop individual tools at deployment time. |
| `internal/observability/` | Prometheus metrics + OTLP tracing init. Per-tool counter + duration histogram and a separate error counter; OTLP no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset. |
| `helm/` | Chart with NetworkPolicy / HPA / VPA / PDB opt-ins, four overlays (memory / valkey / rbac-minimal / autoscaling). |

## Threat model — what the boundaries protect

**Phase 1 (today): single server-admin SA per MCP pod.** The MCP holds
one Grafana service-account token with server-admin rights. Authz isolates
*callers* per org (a viewer of org A can't query datasources in org B),
but a process compromise gives the attacker every org. The Phase-2 fix
(per-org SAs, listed under "Post-v0.1.0 priorities" in the roadmap)
needs `observability-operator` coordination and is deferred past v0.1.0.

**OAuth trust boundary.** Tokens are validated against `OAUTH_DEX_ISSUER_URL`
(or an SSO forwarder when `OAUTH_TRUSTED_AUDIENCES` is configured). The
`RequireCaller` middleware fails closed on calls without a caller —
catches a future tool that forgets `RequireOrg` or any other handler bug
that lets an unauthenticated request through. `Authorizer.RequireOrg`
then derives the caller from context and checks role + org membership.

**Datasource proxy.** `validateDatasourceProxyPath` blocks `..`, leading
slashes, oversized paths, and URL-encoded traversal (`%2e%2e`). Defence
in depth — Grafana validates again. The grafana client caps response
bodies at 16 MiB so a misbehaving datasource can't OOM the pod.

**Tool-call attribution.** `Instrument` middleware emits an OTEL span,
a Prometheus counter+histogram per tool (plus a separate error counter),
and a structured `tool_call` slog line per invocation. The slog line
carries the caller's OIDC subject, tool name, args, error flag, duration,
and `caller_token_source` (`oauth` vs `sso`) — useful when no OTLP
endpoint is wired up. An MCP gateway can correlate logs and traces via
`trace_id` / `span_id`.

**Authz freshness.** Org membership and role changes propagate within
~30s (the cache TTL). A revoked role can still issue tool calls until
its cache entry expires — accepted trade-off vs paying the Grafana
lookup latency on every call.

**Stdio transport** bypasses OAuth — there's no HTTP listener. Tool calls
hit `RequireCaller` and fail unless the stdio session installs a
synthetic caller. Document this as "local dev / trusted CLI invokers
only"; do not expose stdio to untrusted users.

## Where to add a new read-only tool

**First, check upstream.** Before adding a local handler, look at
`grafana/mcp-grafana` for a tool with the same intent. If it exists,
register it via `gfBinder` (Loki/Prometheus/dashboards/alert-rules all
do this). For Tempo, the corresponding tool likely already lives in
Tempo's own MCP server (`/api/mcp`); the `tempo.go` bridge picks it up
automatically. The *only* place we add local code is when neither
upstream surface has an equivalent (today: Alertmanager v2, `list_orgs` —
see `internal/tools/doc.go` for the rationale per category).

### Path A — delegate to an upstream tool (preferred)

Add the registration to the matching `register*Tools` in the
appropriate file. For tools that take an `org` and a datasource UID
upstream:

```go
import (
    mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
    "github.com/giantswarm/mcp-observability-platform/internal/authz"
    "github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

b.bindDatasourceTool(s, authz.RoleViewer, authz.TenantTypeData,
    grafana.DSTypePrometheus, datasourceUIDArg, mcpgrafanatools.QueryPrometheus)
```

`gfBinder` picks the org's first datasource of the given plugin type
via live `ListDatasources` and injects its UID server-side. The
LLM-visible schema keeps upstream's `datasourceUid` (demoted to
optional, with a hint pointing at `list_datasources`) so the LLM can
override the default to scope a query to a specific tenant; caller-
supplied UIDs are validated against the org's live list and the
expected plugin type. The `tenantType` argument gates the call to
orgs that carry that tenant type (`TenantTypeData` for
metrics/logs/traces/rules; `TenantTypeAlerting` is reserved for
Alertmanager-shaped tools, which today are local). Tools whose
upstream arg name isn't `datasourceUid` (e.g. `alerting_manage_rules`
uses `datasource_uid`) pass that string explicitly as the `argName`
parameter.

For org-only upstream tools (no datasource), use `b.bindOrgTool(s, role, t)`.

### Path B — local handler (only when upstream has no equivalent)

Document why in the file header. Then add the registration through
`maybeAddTool` so the tool honours `--disabled-tools`:

```go
maybeAddTool(s, disabled,
    mcp.NewTool("your_tool_name",
        readOnlyAnnotation(),
        mcp.WithDescription("..."),
        orgArg(),
        // ... other args
    ),
    func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        orgRef, err := req.RequireString("org")
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        org, err := az.RequireOrg(ctx, orgRef, authz.RoleViewer)
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        // … call gc (grafana.Client) using grafanaOpts(ctx, org.OrgID)
        return mcp.NewToolResultJSON(result)
    },
)
```

For datasource-proxied local tools, see `resolveDatasource` in
`internal/tools/datasource.go` — it bundles the org→DS-ID lookup with the
tenant-type and role checks the local handlers all need.

### Always

- **Don't accept secret arguments.** Look credentials up server-side
  from the caller identity. The structured `tool_call` log line
  emitted by `Instrument` records args verbatim; secrets in args
  would land in the cluster log pipeline.
- **Tests.** `handler_authz_test.go` enumerates every registered
  tool that takes an `org` argument and asserts the deny path —
  delegated tools are covered automatically. Add a happy-path
  integration test in `handler_integration_test.go` only for tools
  with non-trivial response shaping that's still local.

## Out of scope

See [`roadmap.md` § Out of scope](./roadmap.md#out-of-scope) for the
canonical list. Top hits: multi-cluster federation, generic
non-Grafana datasource clients, custom error envelopes, result caching
beyond the resolver cache, in-tree SBOM/CodeQL.
