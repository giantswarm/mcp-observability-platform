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
              │   • verifies bearer signed by DEX_ISSUER_URL                   │
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
              │   2. Instrument         span + metric + audit, single Classify │
              │   3. RequireCaller      fail-closed if no caller in ctx        │
              │   4. ResponseCap        replace oversized text with structured │
              │   5. ToolTimeout        per-handler context deadline           │
              └──────────────────────────┬─────────────────────────────────────┘
                                         ▼
              ┌────────────────────────────────────────────────────────────────┐
              │ Tool handler (internal/tools/*.go)                             │
              │                                                                │
              │ Bridged tools (most of the surface):                           │
              │   internal/tools/upstream.Bridge.Wrap{,Datasource}             │
              │     1. read "org" from CallToolRequest                         │
              │     2. az.RequireOrg(ctx, org, role) → Organization            │
              │     3. WrapDatasource: pick DS by Kind, look up its UID via    │
              │        grafana.LookupDatasourceUIDByID, inject datasourceUid   │
              │     4. attach mcpgrafana.GrafanaConfig (OrgID, X-Grafana-User) │
              │     5. delegate to upstream grafana/mcp-grafana handler        │
              │                                                                │
              │ Local tools (Tempo, Alertmanager v2, silences, triage,         │
              │ list_orgs, explain_query) read args, az.RequireOrg, then       │
              │ call grafana.Client.DatasourceProxy directly.                  │
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
| `cmd/` | Cobra CLI + per-concern builders (`oauth.go`, `orgregistry.go`, `mux.go`, `serve.go`). `runServe` wires everything in deterministic phase order. |
| `internal/server/` | MCP server construction; middleware composition (Instrument / RequireCaller / ResponseCap / ToolTimeout); transport wrappers for streamable-HTTP and SSE. |
| `internal/server/middleware/` | One file per middleware. `Classify()` is the shared outcome bucketer feeding span / metric / audit. |
| `internal/authz/` | Caller identity (`caller.go`), role enum (`role.go`), org-access types, `Authorizer` interface (`authorizer.go`) + LRU/singleflight cache. `OrgRegistry` is a domain port — the K8s informer adapter sits in `cmd/orgregistry.go`. |
| `internal/audit/` | Structured tool-call records on stderr. Args are size-capped (4 KiB / 16 KiB). |
| `internal/grafana/` | HTTP client. `RequestOpts{OrgID, Caller}` is set per call; `validateDatasourceProxyPath` guards against traversal. `LookupDatasourceUIDByID` is the per-call ID→UID resolver the upstream bridge uses. |
| `internal/tools/` | One file per tool category. Bridged: `dashboards.go`, `metrics.go`, `logs.go`, `alerting.go` (and bridged datasource tools in `orgs.go`). Local: `orgs.go` (list_orgs), `alerts.go`, `silences.go`, `traces.go`, `triage.go`. Shared: `datasource.go`, `pagination.go`, `tools.go`. |
| `internal/tools/upstream/` | Bridge to upstream `grafana/mcp-grafana` tool handlers. `Bridge.Wrap` covers org-only tools; `Bridge.WrapDatasource` covers tools that need a datasource UID (resolves it via `grafana.LookupDatasourceUIDByID` and injects it server-side so the LLM keeps the simple `{org, ...}` shape). `WithOrg` / `WithOrgReplacingArg` rewrite the upstream input schema accordingly. |
| `internal/observability/` | Prometheus metrics + OTEL tracing/logs init. Three-bucket outcome metrics (`ok` / `user_error` / `system_error`). |
| `helm/` | Chart with NetworkPolicy / HPA / VPA / PDB opt-ins, four overlays (memory / valkey / rbac-minimal / autoscaling). |

## Threat model — what the boundaries protect

**Phase 1 (today): single server-admin SA per MCP pod.** The MCP holds
one Grafana service-account token with server-admin rights. Authz isolates
*callers* per org (a viewer of org A can't query datasources in org B),
but a process compromise gives the attacker every org. The Phase-2 fix
(per-org SAs, listed under "Post-v0.1.0 priorities" in the roadmap)
needs `observability-operator` coordination and is deferred past v0.1.0.

**OAuth trust boundary.** Tokens are validated against `DEX_ISSUER_URL`
(or an SSO forwarder when `OAUTH_TRUSTED_AUDIENCES` is configured). The
`RequireCaller` middleware fails closed on calls without a caller —
catches a future tool that forgets `RequireOrg` or any other handler bug
that lets an unauthenticated request through. `Authorizer.RequireOrg`
then derives the caller from context and checks role + org membership.

**Datasource proxy.** `validateDatasourceProxyPath` blocks `..`, leading
slashes, oversized paths, and URL-encoded traversal (`%2e%2e`). Defence
in depth — Grafana validates again. The grafana client caps response
bodies at 16 MiB so a misbehaving datasource can't OOM the pod.

**Audit attribution.** Every tool call emits a JSON record with the
caller's OIDC subject, tool name, args (size-capped), outcome, duration,
and `caller_token_source` (`oauth` for own tokens vs `sso` for forwarded
ones). Goes to stderr; the cluster log pipeline ships it onward.

**Authz freshness.** Org membership and role changes propagate within
~30s (positive cache TTL); shorter for negatives (5s). A revoked role
can still issue tool calls until its cache entry expires — accepted
trade-off vs paying the Grafana lookup latency on every call.

**Stdio transport** bypasses OAuth — there's no HTTP listener. Tool calls
hit `RequireCaller` and fail unless the stdio session installs a
synthetic caller. Document this as "local dev / trusted CLI invokers
only"; do not expose stdio to untrusted users.

## Where to add a new read-only tool

**First, check upstream.** Before adding a local handler, look at
`grafana/mcp-grafana` for a tool with the same intent. If it exists,
register it through the bridge (Loki/Prometheus/dashboards/alert-rules
all do this). This inherits upstream maintenance for free; the
*only* place we add local code is when upstream has no equivalent
(today: Tempo, Alertmanager v2, silences, list_orgs, triage co-pilots,
explain_query — see `internal/tools/doc.go` for the rationale per
category).

### Path A — bridge an upstream tool (preferred)

Add the registration to the matching `register*Tools` in the
appropriate file. For tools that take an `org` and a datasource UID
upstream:

```go
import (
    mcpgrafanatools "github.com/grafana/mcp-grafana/tools"
    "github.com/giantswarm/mcp-observability-platform/internal/authz"
    "github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

t := mcpgrafanatools.QuerySomething
s.AddTool(
    upstream.WithOrgReplacingDatasource(t.Tool),
    br.WrapDatasource(authz.RoleViewer, authz.DSKindMimir, t),
)
```

The bridge resolves `org → OrgID + datasource UID` server-side, so
the LLM-visible schema is upstream's verbatim minus `datasourceUid`,
plus our `org`. Tools whose upstream arg name isn't `datasourceUid`
(e.g. `alerting_manage_rules` uses `datasource_uid`) use the
`WithOrgReplacingArg` / `WrapDatasourceArg` pair.

For org-only upstream tools (no datasource), use `upstream.WithOrg` +
`br.Wrap`.

### Path B — local handler (only when upstream has no equivalent)

Document why in the file header. Then add the registration:

```go
s.AddTool(
    mcp.NewTool("your_tool_name",
        ReadOnlyAnnotation(),
        mcp.WithDescription("..."),
        mcp.WithString("org", mcp.Required(), mcp.Description("...")),
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

For datasource-proxied tools, reuse `datasourceProxyHandler(az, gc,
datasourceSpec{...})` instead of hand-rolling.

### Always

- **Don't accept secret arguments.** Look credentials up server-side
  from the caller identity. The audit stream stays clean by
  construction; the package doc on `internal/audit` spells out the
  rule.
- **Tests.** `handler_authz_test.go` enumerates every registered
  tool that takes an `org` argument and asserts the deny path —
  bridged tools are covered automatically. Add a happy-path
  integration test in `handler_integration_test.go` only for tools
  with non-trivial response shaping that's still local.

## Out of scope

See [`roadmap.md` § Out of scope](./roadmap.md#out-of-scope) for the
canonical list. Top hits: multi-cluster federation, generic
non-Grafana datasource clients, custom error envelopes, result caching
beyond the resolver cache, in-tree SBOM/CodeQL.
