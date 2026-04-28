# Roadmap

Forward-looking work plan. For what's already shipped:

- [`README.md`](../README.md) — architecture layout, MCP surface, configuration, metrics.
- [`CHANGELOG.md`](../CHANGELOG.md) — per-release feature list.
- `git log` — authoritative history.
- [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md) — request flow, package layout, threat model.

## Post-v0.1.0 priorities

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

### 2. Datasource UID + kind in `GrafanaOrganization.status`

Drops the ID→UID round-trip we do per delegated tool call. Today
`internal/grafana/client.go.LookupDatasourceUIDByID` is invoked for
every datasource-scoped delegated tool because the CR status only
carries `{ID, Name}` and our substring-matching kind detection
(`internal/grafana/datasource.go.MatchKind`) compensates.

- `observability-operator` publishes `status.dataSources[i].uid` and
  `status.dataSources[i].kind` alongside the existing `id` / `name`.
- `grafana.Datasource` grows `UID` and `Kind` fields; `Organization.
  FindDatasource` reads `Kind` directly instead of substring-matching
  `Name`.
- `gfBinder.bindDatasourceTool` injects the UID directly, removing
  the per-call Grafana lookup.
- `internal/grafana/client.go.LookupDatasourceUIDByID` and
  `MatchKind` become dead and are deleted.

### 3. Write tools gated on Editor / Admin

The authz model (`Role` with Editor/Admin,
`GrafanaOrganization.spec.rbac.editors/admins`) supports this;
`Instrument` middleware already audits payloads. Highest-value
writes that match upstream:

- `create_silence(org, matchers, duration, comment)` — gated on
  Editor.
- `create_annotation(org, dashboardUid?, text, tags[])` — bot-driven
  deploy annotations.
- `update_annotation(org, id, ...)` — partial-update.

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

### 5. Delegate Tempo tools to Tempo's MCP server

Tempo ships its own MCP server (`query_frontend.mcp_server.enabled`,
`/api/mcp`, streamable-HTTP) with `traceql-search`, `get-trace`,
`get-attribute-names`, `get-attribute-values`,
`traceql-metrics-instant`, `traceql-metrics-range`, `docs-traceql`.
`mcp-grafana@v0.12.0` already implements the MCP-to-MCP proxy via
Grafana's datasource proxy (`proxied_tools.go`,
`mcpgrafana.NewToolManager`, `WithProxiedTools(true)`).

**Migration gain:** four tools we don't have today (`get-trace`,
`traceql-metrics-instant`, `traceql-metrics-range`, `docs-traceql`)
plus vocab alignment (`tag` → `attribute`).

**Prereq:** Tempo's MCP server enabled uniformly across all tenants
we serve. The per-tenant opt-in means partial coverage today —
local handlers stay until that closes.

**Migration shape:** wire
`mcpgrafana.NewToolManager(sm, mcpServer, WithProxiedTools(true))`
at server build, call `InitializeAndRegisterServerTools(ctx)` (or the
per-session variant for HTTP/SSE), delete `internal/tools/traces.go`.
Open question to revisit at migration time: ToolManager registers
tools as `tempo_traceql-search(datasourceUid, ...)`, not
`query_traces(org, ...)` — accept upstream's surface, or build a
wrapper that re-emits with our `org` convention.

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
