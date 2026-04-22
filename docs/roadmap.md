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
| Transports + config validators | All three MCP transports wired (`streamable-http` default, `sse`, `stdio`). Config validators reusing mcp-oauth exports. Encryption-key entropy check. `MCP_OAUTH_TRUSTED_AUDIENCES` / `MCP_OAUTH_TRUSTED_REDIRECT_SCHEMES` for SSO token forwarding + CLI redirect schemes | #19 |
| Resolver cache correctness | Singleflight-collapsed stampedes; cache keyed on OIDC `sub`; split positive/negative TTLs (30s / 5s); bounded by `hashicorp/golang-lru/v2`; slice-cloned returns; distinct `ErrOrgNotFound` vs `ErrNotAuthorised`; informer-alive atomic bool feeding the `k8s_cache` readiness probe; `Caller.Login` field deleted | #21 |
| Tool layer cleanup + upstream alignment | Stop mutating `req.Params.Arguments` in re-dispatching handlers (audit records now show caller's actual args); uniform `response_too_large` payload; tool renames to match `grafana/mcp-grafana` (`query_prometheus`, `query_loki_logs`, `search_dashboards`); new `search_folders` + `get_annotation_tags`; deleted `DatasourceProxyPOST` + `doPOSTForm` + `prompts.go` + `resources.go` + `annotations.go` + `metricLabelValuesHandler`; `GrafanaProxyDuration` gained a `status` label and error-path observation | #22 |
| Authz package split + deep-clone | `internal/authz/` split into `resolver.go` / `cache.go` / `role.go` / `access.go` / `caller.go` / `errors.go` (each file is now one concept). `cloneOrgAccess` uses CR-generated `DeepCopyInto` so handler mutations of `Tenants[i].Types` (or other nested slices) can't escape into the cache | #23 |
| Response-cap as middleware + YAGNI sweep + doc fix | Response cap moved from ~22 per-handler call sites into `middleware.ResponseCap()`. `datasourceSpec.ForceRange` + `DefaultRangeAgo` (single caller each) removed, `invocationFromRequest` inlined. README "Prompts" + "Resource templates" sections deleted (they advertised things that don't register); all 34 registered tools listed under current names. Package-doc comments added to every `internal/tools/*.go` file | #24 |

## Orientation — pre-release cleanup

Nothing is deployed yet, so **backwards compatibility is not a constraint**.
The Tier 1 block below is six PRs of cleanup + upstream-alignment work
targeting v0.1.0. Tier 2 is optional follow-up that doesn't block the
release. Glossary of jargon used in the Tier 1 descriptions:

- **Stampede / thundering herd** — N concurrent callers all miss a cold
  cache entry and each call upstream instead of sharing one round-trip.
  Fixed with `singleflight`.
- **Hexagonal boundary** — domain types shouldn't re-export
  infrastructure types. The domain declares a port (interface); the
  adapter (K8s client, HTTP, etc.) satisfies it. Keeps refactors local
  and tests cheap.
- **Compute-once pattern** — derive a value in one middleware, stash on
  `context.Context`, downstream sinks read back. Prevents drift between
  metrics / spans / audit records.

## Tier 1 — ship next (before v0.1.0)

Four PRs remaining. PRs 10 and 11 both touch `cmd/serve.go` so are
serialised; PR 13 depends on PR 10's compute-once middleware. Total
≈ 2000 LOC across the four.

### PR 10 — Observability + audit + OTLP logs + metric namespace (~500 LOC)

- **Compute outcome classification once**, fan out to Tracing / Metrics
  / Audit via a context value. Today each middleware reclassifies
  independently — a future middleware using a different `Classify` would
  drift silently. Central computation makes the contract compile-checked.
- **Pre-compute label-bound metric instruments per tool.** Curry
  `WithLabelValues(name, …)` at wrap time; cache the three
  label-specialised instruments per tool. Drops per-call map-lookup
  allocations.
- **Audit gets `caller_provider` field** to prevent SIEM collision when
  two identities have the same email from different issuers.
- **Audit `args` size cap** — truncate string values > 4 KiB, total
  serialised length > 16 KiB. Loki default ingest is 256 KiB; stay
  well under. Uses existing `Redactor` hook.
- **Tracer provider stops being global.** `InitTracing` returns
  `*sdktrace.TracerProvider` instead of calling
  `otel.SetTracerProvider(tp)`. Fixes cross-package test contamination.
- **Real histogram buckets.** Replace `ExponentialBuckets(0.01, 2.5, 10)`
  (which gives nothing between 38s and ∞) with
  `{.025, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, +Inf}` for tool calls and
  `prometheus.DefBuckets` for the Grafana proxy.
- **Metric namespace shortening** `mcp_observability_platform_*` → `mcp_*`.
  All families rename: `mcp_tool_call_total`,
  `mcp_tool_call_duration_seconds`, `mcp_grafana_proxy_total`,
  `mcp_grafana_proxy_duration_seconds`, `mcp_org_cache_size`, plus new
  `mcp_resolver_cache_entries` / `mcp_resolver_cache_upstream_calls_total`
  from PR 8. No external dashboards to break.
- **OTLP logs via `otelslog`.** New `internal/observability/logging.go`
  wires `go.opentelemetry.io/contrib/bridges/otelslog` +
  `otlploghttp` onto the same `OTEL_EXPORTER_OTLP_ENDPOINT` as traces.
  Free `trace_id` / `span_id` correlation on every log record. No-op
  when endpoint unset.

Deletion: `firstNonEmpty` helper — use `cmp.Or` (already used
throughout).

### PR 11 — Hexagonal boundaries + cmd/serve polish (~700 LOC)

**Hexagonal cleanup:**

- **`OrgAccess` stops re-exporting Kubernetes CR types.** Today
  `OrgAccess.Tenants []obsv1alpha2.TenantConfig` and
  `Datasources []obsv1alpha2.DataSource` pull
  `observability-operator/api/v1alpha2` and transitively
  `controller-runtime` into every `internal/tools/*.go` consumer.
  Introduce domain types `authz.Tenant` / `authz.Datasource`; translate
  in `toOrgAccess`.
- **Narrow the `ctrlclient.Reader` port** to a consumer-owned
  `OrgRegistry { List(ctx); Lookup(ctx, ref) }`. Resolver tests drop
  from "build a fake ctrlclient" to implementing two methods.
- **Rename `GrafanaOrgLookup` → `OrgMembershipLookup`.** Consumer-side
  naming shouldn't reference the adapter.
- **Drop JSON tags from `OrgAccess`.** Define wire DTOs at the
  tool-handler layer where marshalling actually happens.
- **`Role.AtLeast(other)` method** replaces silent `oa.Role < minRole`
  iota comparison.

**cmd/serve polish:**

- **Require hex-64 or base64-44 for `OAUTH_ENCRYPTION_KEY`** — drop the
  raw-32-bytes branch. README documents `openssl rand -hex 32`.
- **Env var prefix `MCP_OAUTH_*` → `OAUTH_*`** to match mcp-oauth's
  `oauthconfig` package convention + mcp-kubernetes sibling.
- **`envBool` warns on parse error** (today `DEBUG=yes` silently becomes
  false).
- **HTTP server timeouts** — `IdleTimeout: 60s` on both servers;
  `WriteTimeout: 10s` on obsServer only (MCP streams can be long-lived).
- **JSON logs in Kubernetes** — default to JSON handler when
  `KUBERNETES_SERVICE_HOST` is set; `LOG_FORMAT=json|text` override.
- **`OrgCacheSize` gauge via informer EventHandler** — swap the 30s
  polling goroutine for `OnAdd`/`OnDelete` handlers.
- **Health handler hardening** — marshal to buffer first (don't ship
  half-written JSON on encoder error); use `statusOK` constant;
  `HTTPProbe` requires `200 ≤ code < 300` (not `< 300`, which accepts
  1xx).
- **Helm `PodDisruptionBudget` smart default** — gate
  `templates/poddisruptionbudget.yaml` on `replicas > 1` (today opt-in).

### PR 12 — Sift-equivalent co-pilot tools (~200 LOC)

Thin API wrappers force the LLM to write PromQL / LogQL / TraceQL.
Higher-level composites improve success on vague questions. OSS
equivalents of upstream Sift tools (Grafana-Cloud-only).

- **`find_error_pattern_logs(org, service, lookback)`** — auto-selects
  a LogQL selector from service labels + error-keyword filter; calls
  `query_loki_stats` first to avoid `response_too_large`. Matches
  upstream Sift name.
- **`find_slow_requests(org, service, lookback, min_duration?)`** —
  wraps `query_traces` with service filter + duration threshold +
  optional TraceQL `status=error`. Matches upstream Sift name.
- **`explain_query(org, promql)`** — series-count + selectivity estimate
  before the LLM fires an expensive query. Prevents pathological
  queries. No upstream equivalent.

Each ~50 LOC, each reuses existing tools internally.

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

- Jittered retry on idempotent 5xx / connect errors; `sony/gobreaker`
  for circuit-breaker. Rolling Grafana upgrades stop failing in-flight
  MCP calls.
- `io.ReadAll` → `io.LimitReader` with 16 MiB cap.
- Sanitise `opts.Caller` before `X-Grafana-User` header set; strip
  control chars, cap length.
- SSRF defence on `DatasourceProxy` path construction; regex validate,
  reject `..`.
- `HasImageRenderer` — on error, don't advance `rendererAt` cache; retry
  on next call instead of disabling renders for 5 minutes.
- Consistent `%w` error wrapping.
- `authHeader` field → redacted type with `String() → "[REDACTED]"`.
- `BaseURL()` copy-don't-reparse.
- `detectPromError` bounded scan.

~400 LOC. Independent.

### File splits (pure movement)

- `cmd/serve.go:runServe` (~295 LOC) → `buildKubeCache`, `buildOAuth`,
  `buildMCPMux`, `buildObsMux`, `runTwoPhaseShutdown`. Makes `cmd/`
  unit-testable.
- `internal/tools/dashboards.go` (1020 LOC) → `internal/tools/dashparse/`
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

Both bundled into PR 11's dashboards-split work if it lands, otherwise
backfill independently.

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

- **Prompts.** Tried in the original roadmap; dropped. LLMs do fine with
  tool outputs; maintaining prompt templates is higher cost than value.
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

### Candidates to propose upstream to `mcp-oauth`

- **Encryption-key entropy check.** Already drafted in mcp-oauth PR
  #273 (on `feat/oauthconfig-from-env`). Ports our local
  `validateEncryptionKeyEntropy` into `security.NewEncryptor` so every
  downstream caller gets the guard for free.
