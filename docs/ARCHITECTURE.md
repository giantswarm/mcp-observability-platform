# Architecture

One-page reference for contributors. The deeper "what's landed" log lives
in [`roadmap.md`](./roadmap.md); the per-tool surface is in
[`README.md`](../README.md). This file is for orientation: where things
live, what the security boundaries are, and where to add a new tool.

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
              │   • parses args from CallToolRequest                           │
              │   • az.RequireOrg(ctx, orgRef, role) → resolved Organization   │
              │   • picks the right datasource by tenant + name match          │
              │   • calls grafana.Client (proxies to Mimir/Loki/Tempo/AM)      │
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
| `internal/grafana/` | HTTP client. `RequestOpts{OrgID, Caller}` is set per call; `validateDatasourceProxyPath` guards against traversal. |
| `internal/tools/` | One file per tool category (`orgs.go`, `dashboards*.go`, `metrics.go`, `logs.go`, `traces.go`, `alerts.go`, `silences.go`, `triage.go`). `datasource.go` holds the shared proxy dispatcher. |
| `internal/observability/` | Prometheus metrics + OTEL tracing/logs init. Three-bucket outcome metrics (`ok` / `user_error` / `system_error`). |
| `helm/` | Chart with NetworkPolicy / HPA / VPA / PDB opt-ins, four overlays (memory / valkey / rbac-minimal / autoscaling). |

## Threat model — what the boundaries protect

**Phase 1 (today): single server-admin SA per MCP pod.** The MCP holds
one Grafana service-account token with server-admin rights. Authz isolates
*callers* per org (a viewer of org A can't query datasources in org B),
but a process compromise gives the attacker every org. The Phase-2 fix
(per-org SAs, Tier 3 in the roadmap) needs `observability-operator`
coordination and is deferred past v0.1.0.

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

Canonical example: `internal/tools/orgs.go` `list_datasources`. Steps:

1. **Pick the file** that matches the category (`metrics.go` for
   PromQL / Mimir, `logs.go` for LogQL / Loki, `dashboards.go` for
   Grafana dashboards, …).
2. **Define the tool** inside the existing `register*Tools` function:
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
3. **For datasource-proxied tools** (Mimir / Loki / Tempo / Alertmanager),
   reuse `datasourceProxyHandler(d, datasourceSpec{...})` instead of
   hand-rolling — see `metrics.go` `query_prometheus` for the pattern.
4. **Don't accept secret arguments.** Look credentials up server-side
   from the caller identity. The audit stream stays clean by
   construction; the package doc on `internal/audit` spells out the
   rule.
5. **Add an integration test** in `internal/tools/handler_integration_test.go`
   if the tool has non-trivial response shaping; the table-driven
   `handler_authz_test.go` already covers the deny path generically.

## Out of scope

See [`roadmap.md` § Out of scope](./roadmap.md#out-of-scope) for the
canonical list. Top hits: multi-cluster federation, generic
non-Grafana datasource clients, custom error envelopes, result caching
beyond the resolver cache, in-tree SBOM/CodeQL.
