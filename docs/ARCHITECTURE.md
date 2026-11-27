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
              │ Official SDK propagates the validated UserInfo context         │
              │   authorization and audit use the same upstream user           │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ Native official SDK server (internal/server):                  │
              │   1. Instrument         Prometheus counters and histogram      │
              │   2. Shared policy      audit, duration, deadline, result cap  │
              │   3. RequireCaller      fail closed on incomplete identity     │
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
              │        (native SDK request and result types)                  │
              │                                                                │
              │ Local tools (Alertmanager v2) call az.RequireOrg then          │
              │ grafana.Client.DatasourceProxy directly. list_orgs uses        │
              │ az.ListOrgs (no datasource).                                   │
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
| `cmd/` | CLI and composition root. OAuth configuration and lifecycle use `oauthconfig.SettingsFromEnv` and `Setup`, with application defaults and compatibility for old issuer keys applied before background workers start. This package owns Grafana credentials, the Kubernetes informer adapter, pod-local readiness and the MCP and observability listeners. |
| `internal/server/` | Native SDK server construction, shared toolkit policy, and streamable HTTP / SSE transport composition. Streamable HTTP is stateless. SSE uses a native session registry per validated caller profile. |
| `internal/server/middleware/` | Authentication gate and the existing Prometheus tool counters and histogram. Authentication reads mcp-oauth's validated `providers.UserInfo` directly. |
| `internal/authz/` | Grafana lookup identity, role and organization access, and a bounded role cache keyed by issuer, stable subject and verified lookup identity. `OrgLister` is implemented by the Kubernetes informer adapter in `cmd/orglister.go`. |
| `internal/grafana/` | HTTP client (`VerifyServerAdmin`, `LookupUser`, `UserOrgs`, `ListDatasources`, `LookupDatasourceByUID`, `DatasourceProxy`) plus `Datasource` and `DatasourceType` (`MatchesType` / `FilterDatasourcesByType`). Delegated tools talk to upstream's `mcpgrafana.GrafanaClient` instead of this client. `RequestOpts{OrgID, Caller}` is set per call; `validateDatasourceProxyPath` guards against traversal. `ListDatasources` is cached per-OrgID (30s TTL); `LookupDatasourceByUID` inherits transparently. |
| `internal/tools/` | Native local handlers and delegation to `grafana/mcp-grafana`. `gfBinder` checks organization access, selects the datasource and passes the native request directly to the upstream handler. It copies arguments without changing JSON number precision, continuation state, transport context or native results. `maybeAddTool` applies `--disabled-tools`. |
| `internal/observability/` | Prometheus metrics + OTLP tracing init. Per-tool counter + duration histogram and a separate error counter; OTLP no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset. |
| `helm/` | Chart with NetworkPolicy / HPA / VPA / PDB opt-ins, four overlays (memory / valkey / rbac-minimal / autoscaling). |

## Threat model — what the boundaries protect

**Phase 1 (today): single server-admin SA per MCP pod.** The MCP holds
one Grafana service-account token with server-admin rights. Authz isolates
*callers* per org (a viewer of org A can't query datasources in org B),
but a process compromise gives the attacker every org. The Phase-2 fix
(per-org SAs, listed under "Post-v0.1.0 priorities" in the roadmap)
needs `observability-operator` coordination and is deferred past v0.1.0.

**JWT mode (`GRAFANA_AUTH_MODE=jwt`): no admin credential.** The MCP
forwards each caller's own Dex `id_token` to Grafana. Grafana enforces
the caller's own org roles. A process compromise exposes only the tokens
of active callers held in the OAuth token store, each limited to that
user's Grafana access. Requires Grafana `[auth.jwt]` against Dex.
Rotating the Dex client secret prevents further refreshes with the old
client credential. Already-issued tokens remain valid until expiry unless
their verifier revokes access. Restarting the MCP does not revoke tokens.

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

**Tool-call attribution.** Shared toolkit policy records
`mcp.server.operation.duration` and a `tool_call` audit entry containing
validated issuer, subject and supplied actor fields under `user`, plus tool and
outcome. Profile fields,
argument values, raw errors and credentials are excluded. HTTP tracing and
SDK trace propagation retain request correlation. Existing Prometheus tool
counters and duration histograms remain available.

**Authz freshness.** Org membership and role changes propagate within
~30s (the cache TTL). A revoked role can still issue tool calls until
its cache entry expires — accepted trade-off vs paying the Grafana
lookup latency on every call. The bounded cache separates issuer, subject
and the verified lookup identity; changing a verified email forces a fresh
Grafana lookup. Unverified email never supplies the Grafana identity.

**Stdio transport** bypasses OAuth — there's no HTTP listener. Tool calls
hit `RequireCaller` and fail unless the stdio session installs a
synthetic caller. Document this as "local dev / trusted CLI invokers
only"; do not expose stdio to untrusted users.

## Where to add a new read-only tool

**First, check upstream.** Before adding a local handler, look at
`grafana/mcp-grafana` for a tool with the same intent. If it exists,
register it via `gfBinder` (Loki/Prometheus/Tempo/dashboards/alert-rules
all do this). The *only* place we add local code is when neither
upstream surface has an equivalent (today: Alertmanager v2, `list_orgs` —
see `internal/tools/doc.go` for the rationale per category).

### Path A — delegate to an upstream tool (preferred)

Add the registration to the matching `register*Tools` in the
appropriate file. For tools that take an `org` and a datasource UID
upstream:

```go
import (
    mcpgrafanatools "github.com/grafana/mcp-grafana/v2/tools"
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
upstream arg name isn't `datasourceUid` (e.g. `alerting_rules_read`
uses `datasource_uid`) pass that string explicitly as the `argName`
parameter.

For org-only upstream tools (no datasource), use `b.bindOrgTool(s, role, t)`.

If upstream's description opens with a line longer than 120 runes (all muster's `filter_tools` shows), add a summary line for the tool in `internal/tools/summaries.go`, with the hash `TestUpstreamSummariesAreCurrent` reports for upstream's current text. That test fails whenever an mcp-grafana bump changes the text: re-read the handler, check the summary, update the hash, and run `make tools-doc`.

### Path B — local handler (only when upstream has no equivalent)

Document why in the file header. Then add the registration through
`maybeAddTool` so the tool honours `--disabled-tools`:

```go
maybeAddTool(s, disabled,
    mcp.NewTool("your_tool_name",
        readOnlyAnnotation(),
        tooldesc.Describe("Summary line, at most 120 runes.", "Details, if a model needs them before calling."),
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
