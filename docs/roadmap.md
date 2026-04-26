# Roadmap

Forward-looking work plan for production-hardening the MCP on top of what's
landed. For what's already shipped:

- [`README.md`](../README.md) — architecture layout, MCP surface, configuration, metrics.
- [`CHANGELOG.md`](../CHANGELOG.md) — per-release feature list.
- `git log` — authoritative for when and how each change landed.
- [`docs/upstream-contributions.md`](./upstream-contributions.md) — parallel contribution lane to `grafana/mcp-grafana`.

## What's landed

| Scope | Detail | PR |
| --- | --- | --- |
| Go scaffold | Cobra CLI, `GrafanaOrganization` CR-backed authz resolver, Grafana admin client, Prometheus metrics, OTEL tracing, mcp-go wiring | #3 |
| Tool surface | 32 MCP tools across orgs / datasources / dashboards / metrics / logs / traces / alerts / silences / panels | #10 |
| Tool middleware + MCP annotations + package restructure | mcp-go `ToolHandlerMiddleware`: `Tracing()` + `Metrics()`, `mcpsrv.WithRecovery()`, `ReadOnlyAnnotation()`, 3-bucket outcome label, `internal/{identity,observability,server/middleware,tools}/` layout | #4 |
| Helm chart | `checksum/config` rollout, runtime ConfigMap via `envFrom`, NetworkPolicy + HPA + VPA + PDB opt-ins, 19 helm-unittest tests, 4 overlays (memory / valkey / rbac-minimal / autoscaling) | #5 |
| CI | CircleCI multi-arch image push + chart publish matching `mcp-kubernetes` shape; hand-written `ci.yaml`; devctl-generated release / scanning workflows | #6 |
| Deep readiness + two-phase shutdown | `/healthz` vs `/readyz` split, JSON `/healthz/detailed`, graceful drain ordered MCP → observability | #7 |
| Audit middleware | Structured JSON per tool call on stderr. Shared `Classify()` feeds audit + metrics + span attributes. Pluggable `Redactor` | #8 |
| Transports + config validators | All three MCP transports wired (`streamable-http` default, `sse`, `stdio`). Config validators reusing mcp-oauth exports. Encryption-key entropy check. `OAUTH_TRUSTED_AUDIENCES` / `OAUTH_TRUSTED_REDIRECT_SCHEMES` for SSO token forwarding + CLI redirect schemes | #19 |
| Resolver cache correctness | Singleflight-collapsed stampedes; cache keyed on OIDC `sub`; split positive/negative TTLs (30s / 5s); bounded by `hashicorp/golang-lru/v2`; slice-cloned returns; distinct `ErrOrgNotFound` vs `ErrNotAuthorised`; informer-alive atomic bool feeding the `k8s_cache` readiness probe; `Caller.Login` field deleted | #21 |
| Tool layer cleanup + upstream alignment | Stop mutating `req.Params.Arguments` in re-dispatching handlers (audit records now show caller's actual args); uniform `response_too_large` payload; tool renames to match `grafana/mcp-grafana` (`query_prometheus`, `query_loki_logs`, `search_dashboards`); new `search_folders` + `get_annotation_tags`; deleted `DatasourceProxyPOST` + `doPOSTForm` + `prompts.go` + `resources.go` + `annotations.go` + `metricLabelValuesHandler`; `GrafanaProxyDuration` gained a `status` label and error-path observation | #22 |
| Authz package split + deep-clone | `internal/authz/` split into `resolver.go` / `cache.go` / `role.go` / `access.go` / `caller.go` / `errors.go` (each file is now one concept). `cloneOrgAccess` uses CR-generated `DeepCopyInto` so handler mutations of `Tenants[i].Types` (or other nested slices) can't escape into the cache | #23 |
| Response-cap as middleware + YAGNI sweep + doc fix | Response cap moved from ~22 per-handler call sites into `middleware.ResponseCap()`. `datasourceSpec.ForceRange` + `DefaultRangeAgo` (single caller each) removed, `invocationFromRequest` inlined. README "Prompts" + "Resource templates" sections deleted (they advertised things that don't register); all 34 registered tools listed under current names. Package-doc comments added to every `internal/tools/*.go` file | #24 |
| Observability + audit hardening + OTLP logs | Tracing / Metrics / Audit collapsed into one composite `middleware.Instrument` so `Classify()` is computed once and span / metric / audit outcomes can't drift. Metric namespace `mcp_observability_platform_*` → `mcp_*` with realistic latency buckets (25 ms → 60 s for tool calls, `DefBuckets` for Grafana proxy). Audit records gain `caller_token_source` (oauth / sso) and a 4 KiB per-value / 16 KiB total args size cap. OTLP logs via `otelslog` bridge fan out slog records (operator + audit) with `trace_id` / `span_id` correlation when `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is set. `firstNonEmpty` → `cmp.Or` | #25 |

## Orientation — pre-release cleanup

Nothing is deployed yet, so **backwards compatibility is not a constraint**.
The Tier 1 block below is cleanup + upstream-alignment work targeting
v0.1.0. Tier 2 is optional follow-up that doesn't block the release.
Glossary of jargon used in the Tier 1 descriptions:

- **Stampede / thundering herd** — N concurrent callers all miss a cold
  cache entry and each call upstream instead of sharing one round-trip.
  Fixed with `singleflight`.
- **Hexagonal boundary** — domain types shouldn't re-export
  infrastructure types. The domain declares a port (interface); the
  adapter (K8s client, HTTP, etc.) satisfies it. Keeps refactors local
  and tests cheap.

## Tier 1 — ship next (before v0.1.0)

Two PRs remaining (12, 13). PR 11's scope landed in two halves: the
hexagonal boundary work in #28 (see "What's landed"), and the `cmd/serve`
polish in #TBD (OAUTH_ENCRYPTION_KEY tightened to hex-64 / base64-44,
`MCP_OAUTH_*` → `OAUTH_*`, `envBool` / `envInt` / `envDuration` fail
startup on malformed values instead of silently defaulting, HTTP server
`IdleTimeout` + obs-server `WriteTimeout`, JSON logs by default inside
Kubernetes with `LOG_FORMAT=json|text` override, health handler marshal-
to-buffer + `HTTPProbe` 2xx-only guard). Items dropped from the original
plan: the `GrafanaOrgLookup` → `OrgMembershipLookup` rename (superseded —
the interface landed as `OrgRegistry` directly), and the `OrgCacheSize`
informer EventHandler swap (current 30s poll is gauge-only and bounded;
not worth the churn).

### PR 12 — Triage co-pilot tools — IN FLIGHT

Three composites that synthesise common SRE-triage questions, mirroring
grafana/mcp-grafana's Sift surface where an upstream equivalent exists
(no Grafana Cloud Sift backend required — composes existing primitives):

- **`find_error_pattern_logs(org, service, lookback?)`** — probes
  service_name → service → job to pick the right Loki label, runs the
  size-estimate first, refuses when bytes > 256 MiB, otherwise
  `query_range` with an error-keyword regex.
- **`find_slow_requests(org, service, lookback?, min_duration?, errors_only?)`**
  — TraceQL `{ resource.service.name = "X" && duration > Y [&& status = error] }`
  via Tempo `api/search`.
- **`explain_query(org, promql)`** — series-count preflight via
  `count(<expr>)`. Warns when count > 10 000. No upstream equivalent.

### PR 13 — HTTP middleware chain + rate limit + OAuth token refresh (~600 LOC)

**HTTP middleware chain** (outer → inner):
`SecurityHeaders` → `CORS` → `HTTPMetrics` → existing
`oauthHandler.ValidateToken` → MCP.

**Rate limit** — per-caller + per-org + global token bucket. Thresholds
from runtime ConfigMap (`RATE_LIMIT_*` already seeded in the chart).
Rejection → `isError: true` with rate-limit text + `Retry-After` hint.

**OAuth token refresh** — active refresh before expiry via
`mcp-oauth.SetTokenRefreshHandler`. Refresh failure returns an auth
error prompting re-authentication (vs today's silent mid-session
expiry).

Files: new `internal/server/httpmiddleware/*.go`, new
`internal/ratelimit/ratelimit.go`, edits to
`internal/authz/resolver.go` and `cmd/serve.go`.

## Tier 2 — follow-up (doesn't block v0.1.0)

### Grafana client hardening

Most items landed in PR #26 (LimitReader 16 MiB, `X-Grafana-User`
sanitisation, SSRF on `DatasourceProxy`, redacted `authHeader`, `%w`
wrapping, bounded `detectPromError`). Cleanup PR (this branch)
removed the `HasImageRenderer` 5-min cache (a one-shot probe per
panel render isn't worth the staleness + cache-poisoning surface)
and shallow-copied `BaseURL()` instead of round-tripping through
`url.Parse`. Real remaining item:

- Jittered retry on idempotent 5xx / connect errors; `sony/gobreaker`
  for circuit-breaker. Rolling Grafana upgrades stop failing in-flight
  MCP calls. ~150 LOC.

### File splits (pure movement)

- `cmd/serve.go:runServe` (~295 LOC) → `buildKubeCache`, `buildOAuth`,
  `buildMCPMux`, `buildObsMux`, `runTwoPhaseShutdown`. Makes `cmd/`
  unit-testable.
- `internal/tools/dashboards.go` (1020 LOC) → `internal/tools/dashboards/`
  for pure helpers (`readJSONPointer`, `expandGrafanaVars`, JSON
  projections with their own tests) + `dashboards.go`, `deeplinks.go`
  for tool registrations.
- Extract histogram-cardinality logic from
  `internal/tools/metrics.go` (538 LOC).

Zero behaviour change. Defer unless someone needs to touch the large
files.

## Tier 3 — post-release features

### Per-org Grafana SA tokens — phase-2 blast-radius fix

Biggest unresolved security gap but deferred past v0.1.0 because it
needs `observability-operator` coordination (new CRD / Secret
conventions) — not purely in this repo. Today one compromised MCP pod
with a server-admin SA exposes every Grafana org.

- Operator provisions per-`GrafanaOrganization` SAs, writes each to a
  namespaced Secret.
- MCP resolver picks the right SA per-request based on
  `OrgAccess.OrgID`.
- `GRAFANA_SA_TOKEN` / `GRAFANA_BASIC_AUTH` remain as bootstrap fallback,
  documented "dev/bootstrap only; never production".

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

Earlier we dropped resources in favour of tools (LLMs handle tools
better). Subscriptions are a push model tools can't do: subscribe to
"firing critical alerts in org X" → MCP pushes updates mid-conversation.
Worth revisiting once MCP clients broadly support resource
subscriptions.

## Test-coverage gaps (ship when convenient)

- `internal/tools/dashboards.go#expandGrafanaVars` — length-DESC sort
  that stops `$cluster` corrupting `$cluster_id`. Table test.
- `internal/tools/dashboards.go#readJSONPointer` — RFC 6901 edge cases
  (`~0` / `~1` escapes, array indexing, non-container traversal).

Bundled into the dashboards-split work listed under Tier 2 "File splits"
if it lands, otherwise backfill independently.

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
- **`ARCHITECTURE.md`** (from #4) — onboarding doc with hex diagram,
  "where to add X" cheat sheet, threat model.

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
[`docs/upstream-contributions.md`](./upstream-contributions.md):

- **US-1** — Per-request `X-Grafana-User` / `X-Grafana-Org-Id`
  pass-through.
- **US-2** — Mimir + Loki recording-rule tools.
- **US-3** — Dedicated Tempo toolset.

### Candidates to propose back to `mcp-kubernetes`

Shared patterns worth upstreaming to the sibling MCP:

- Response-size cap helper + structured `response_too_large` payload.
- `datasourceProxyHandler` + `datasourceSpec` dispatch-table pattern.
- `paginateStrings` (and `Paginated[T]` once PR 9 lands).
- Typed `Role` enum with `MarshalJSON`.
- Controller-runtime-informer-backed authz resolver (once PR 8 hardens
  it).
- Three-bucket `outcome` metric label + shared `Classify()` across
  middlewares.
