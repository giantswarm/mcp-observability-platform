# Roadmap

Forward-looking work plan. For what's already shipped:

- [`README.md`](../README.md) — architecture layout, MCP surface, configuration, metrics.
- [`CHANGELOG.md`](../CHANGELOG.md) — per-release feature list.
- `git log` — authoritative for when and how each change landed.
- [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md) — request flow, package layout, threat model, where to add a new tool.
- [`docs/upstream-contributions.md`](./upstream-contributions.md) — parallel contribution lane to `grafana/mcp-grafana`.

## Post-v0.1.0 priorities

### Per-org Grafana SA tokens — Phase-2 blast-radius fix

Biggest unresolved security gap, deferred past v0.1.0 because it needs
`observability-operator` coordination (new CRD / Secret conventions) —
not purely in this repo. Today one compromised MCP pod with a
server-admin SA exposes every Grafana org.

- Operator provisions per-`GrafanaOrganization` SAs, writes each to a
  namespaced Secret.
- MCP resolver picks the right SA per-request based on
  `OrgAccess.OrgID`.
- `GRAFANA_SA_TOKEN` / `GRAFANA_BASIC_AUTH` remain as bootstrap fallback,
  documented "dev/bootstrap only; never production".

**Next step:** open an `observability-operator` issue describing the
contract so the dependency is visible from both sides.

### Write tools gated on Editor / Admin

The authz model (`authz.Role` with Editor/Admin,
`GrafanaOrganization.spec.rbac.editors/admins`) already supports this;
`middleware.Audit` already emits the records compliance will demand for
writes. Highest-value writes matching upstream:

- `create_silence(org, matchers, duration, comment)` — "silence this for
  2 hours while I fix it." Gated on Editor.
- `add_annotation(org, dashboardUid?, text, tags[])` — bot-driven deploy
  annotations. Matches upstream `create_annotation`.
- `update_annotation(org, id, ...)` — partial-update shape matching
  upstream.

Each carries `destructiveHint: true` in MCP annotations. Rich audit
records capture full payload for forensics.

### MCP resource subscriptions for firing alerts

Resources were dropped earlier in favour of tools (LLMs handle tools
better). Subscriptions are a push model tools can't do: subscribe to
"firing critical alerts in org X" → MCP pushes updates mid-conversation.
Worth revisiting once MCP clients broadly support resource
subscriptions.

## Test-coverage gaps (ship when convenient)

- `internal/tools/dashboards.go#expandGrafanaVars` — length-DESC sort
  that stops `$cluster` corrupting `$cluster_id`. Table test.
- `internal/tools/dashboards.go#readJSONPointer` — RFC 6901 edge cases
  (`~0` / `~1` escapes, array indexing, non-container traversal).
- **OAuth audience-validation contract test.** Asserts that a token
  with an untrusted `aud` claim is rejected by `mcp-oauth.ValidateToken`.
  Needs `mcp-oauth` to expose a usable test hook (or upstreamed test
  helpers); skip-with-issue-link is the fallback.
- **End-to-end OAuth flow integration test.** `httptest`-backed walk
  through `authorize → callback → token → MCP tool call → 401-on-expiry`.
  Currently `cmd/oauth_test.go` only covers storage configs.

Backfill independently if/when the helpers are touched, or once the
upstream hook surface allows clean coverage of the OAuth flow.

## Deferred from landed PRs

- **ExternalSecret template** (from #5) — Dex creds via ESO. Mixing ESO
  with the existing `existingSecret` pattern cleanly is its own design
  call.
- **Auto-release on main merge** (from #6) — replace manual
  `release#vX.Y.Z` flow with an `auto-release.yaml` that patch-bumps on
  merge. Needs skip-if-empty guard, CHANGELOG promotion back to main
  with a bot token, concurrency-serialize.
- **Standalone Go binaries via goreleaser** (from #6) — useful once
  local stdio deployments become a supported use case. Container image
  is the only distribution today.

## Out of scope (explicitly not doing)

- **Multi-cluster / federating MCP.** One MCP per Grafana is a design
  constraint. A federator above it complicates auth, error propagation,
  and observability for marginal benefit. Let the LLM pick the right
  MCP per question.
- **Generic (non-Grafana) Prometheus/Loki/Tempo clients.** The current
  model leverages Grafana's tenant-header injection via its datasource
  proxy; bypassing it re-implements multi-tenancy.
- **Custom error-envelope format.** MCP's `isError: true` + plain text
  is what LLMs are trained on.
- **Result caching beyond the 30s resolver cache.** Invalidation
  complexity outweighs the savings.
- **SBOM / cosign / CodeQL in-tree.** Handled at Giant Swarm org level.
- **Upstream feature parity** with Pyroscope / OnCall / Incident / Sift
  / Asserts in `grafana/mcp-grafana`. Keep the surface focused on Giant
  Swarm's stack.

## Upstream contribution lane

Parallel, non-blocking. Tracked in
[`docs/upstream-contributions.md`](./upstream-contributions.md). All
three issues filed against `grafana/mcp-grafana`:

- [#794](https://github.com/grafana/mcp-grafana/issues/794) — context-
  aware RoundTrippers (per-request `X-Grafana-User` / `X-Grafana-Org-Id`
  via `req.Context()` instead of frozen-at-construction config).
- [#795](https://github.com/grafana/mcp-grafana/issues/795) — Mimir +
  Loki recording-rule tools.
- [#796](https://github.com/grafana/mcp-grafana/issues/796) — dedicated
  Tempo toolset (TraceQL search, tag discovery, metrics).

### v0.2 spike — depend on `grafana/mcp-grafana`

Once #794 lands upstream and exposes the per-request context override,
prototype replacing our local `internal/grafana/client.go` and the
duplicated tool registrations (`search_dashboards`, `query_prometheus*`,
`query_loki*`, etc.) with imports. The local code base shrinks to the
GS-specific layer: authz, audit, multi-tenant org awareness, middleware
composition.

Blockers before this is worth doing:

1. #794 (context-aware RoundTrippers) merges and ships.
2. Upstream registration functions (`AddDashboardTools`,
   `AddPrometheusTools`, etc.) cover the categories we use.
3. We accept Cloud-only transitive deps (`incident-go`,
   `amixr-api-go-client`) in our binary — small bloat, no security
   issue.

If those align, the migration is ~3 PRs of mechanical wrapping.
