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
| CI | CircleCI multi-arch image push + chart publish matching `mcp-kubernetes` shape; hand-written `ci.yaml` (go test, yamllint, helm lint, helm-unittest, govulncheck); `Makefile.custom.mk`; devctl-generated release / scanning workflows; `renovate.json5` Go preset | #6 |
| Deep readiness + two-phase shutdown | `/healthz` (liveness) vs `/readyz` (probes: Grafana, Dex, K8s cache) split, JSON `/healthz/detailed`, graceful drain ordered MCP → observability so kubelet doesn't SIGKILL mid-drain | #7 |
| Audit middleware | Structured JSON per tool call on stderr: `{timestamp, caller, tool, args, outcome, duration_ms, error}`. Shared `Classify()` function feeds audit + metrics + span attributes → cross-signal labels never drift. Pluggable `Redactor` for future tools that take secrets | #8 |
| Transports | All three MCP transports wired: `streamable-http` (default, OAuth-gated, remote), `sse` (OAuth-gated, remote; for SSE-only clients), `stdio` (local-dev / desktop-client integrations, no HTTP listener, trust follows the spawning process). `MCP_TRANSPORT` picks. `cmd/serve.go` split into per-transport entrypoints | #TBD |

## Tier 1 — ship next (biggest leverage)

### Tool-name alignment with upstream `grafana/mcp-grafana`

Breaking rename to converge on upstream conventions before adoption widens.
LLMs trained on upstream docs reach for upstream names first, so the more
our dialect matches, the higher the first-try success rate. Three renames:

| Current | Rename to | Rationale |
| --- | --- | --- |
| `query_metrics` | `query_prometheus` | Matches upstream; signals "PromQL", not generic metric-query |
| `query_logs` | `query_loki_logs` | Matches upstream; disambiguates vs. other log backends |
| `list_dashboards` | `search_dashboards` | Matches upstream; "search" conveys the `query` arg better |

All other tool names either already match upstream (`get_dashboard_*`,
`list_prometheus_*`, `list_loki_*`, `query_prometheus_histogram`,
`query_loki_*`, `get_panel_image`, `generate_deeplink`, `get_annotations`,
`run_panel_query`) or are Giant-Swarm-only (`list_orgs`, `list_alerts`,
`get_alert`, `list_silences`, `list_alert_rules`, `get_alert_rule`,
all Tempo tools) and keep their names.

No backwards-compatible aliases — project adoption is still small, and
aliases rot unless actively maintained. Clean break.

Files: edit `internal/tools/{metrics,logs,dashboards}.go` (the
`mcp.NewTool(...)` call sites). Verify: `tools/list` returns the new names;
no references to the old names remain anywhere in-repo.

### MCP prompts — runbook templates

Pure differentiator vs upstream (which has **zero** prompts). Four
high-value prompts:

- `investigate-alert(org, fingerprint)` — pulls the alert, correlates logs /
  metrics / traces, produces a short triage report. The one that was in the
  original prototype.
- `cardinality-audit(org, metric?)` — which labels are blowing up the TSDB?
  Uses `list_prometheus_label_values` + selector series counts.
- `incident-summary(org, time_window)` — firing alerts + annotations + top-N
  error logs in one pass. Answers "what happened overnight?".
- `query-builder(org, backend, intent)` — guided PromQL / LogQL / TraceQL
  construction with worked examples per backend. Replaces upstream's
  `get_query_examples` tool — a prompt is the natural shape for "give me
  an example query that does X" since the output is a conversation turn,
  not structured data.

Files: `internal/server/prompts.go`. Verify: `prompts/list` returns expected
names; one prompt renders deterministically in a test.

### HTTP middleware chain + rate limit + OAuth token refresh

Go code only — reads ConfigMap fields seeded in #5.

- HTTP middleware chain: `SecurityHeaders` → `CORS` → `HTTPMetrics` → existing
  `oauthHandler.ValidateToken` → MCP server.
- Rate limiting: per-caller + per-org + global token bucket; thresholds from
  ConfigMap; rejections render `isError: true` with rate-limit text.
- OAuth token refresh: active refresh before expiry via mcp-oauth
  `SetTokenRefreshHandler`; refresh failure → auth error prompting re-auth.

Files: new `internal/server/httpmiddleware/{security,cors,metrics,ratelimit}.go`;
new `internal/ratelimit/ratelimit.go`.

Verify: OWASP headers present; load test caps enforced; token-expiry table
test with clock fake; CORS preflight works from a browser origin.

### Co-pilot tools — raise first-try LLM success rate

The current 32 tools are thin API wrappers; LLMs still need to write
PromQL / LogQL / TraceQL. Composite higher-level tools dramatically improve
success on vague user questions.

Names align with upstream `grafana/mcp-grafana`'s **Sift** tools where an
equivalent exists so an LLM trained on upstream docs reaches for the same
tool in both MCPs. Sift is Grafana Cloud proprietary (disabled by default
upstream, unusable outside Grafana Cloud), so our OSS implementations are
genuinely additive — not forks.

- `find_error_pattern_logs(org, service, lookback)` — auto-selects a LogQL
  selector from service labels, applies an error-keyword filter, calls
  `query_loki_stats` first to avoid `response_too_large`.
  **Matches upstream Sift `find_error_pattern_logs`** (same name, same
  intent; ours backs onto Loki directly instead of Sift's proprietary
  pattern-detection service).
- `find_slow_requests(org, service, lookback, min_duration?)` — wraps
  `query_traces` with a service filter + duration threshold + TraceQL
  `status=error` option. Returns the top-N slow spans with service, duration,
  error status.
  **Matches upstream Sift `find_slow_requests`** (same name; ours queries
  Tempo directly, upstream's calls Sift).
- `compare_metric_trends(org, metric, window, vs_window)` — "API latency now
  vs yesterday"-class RCA questions. Returns percent change per label group.
  **No upstream equivalent** (nor in Sift); pure Giant Swarm addition.
- `explain_query(org, promql)` — series-count + selectivity estimate before
  the LLM fires an expensive query. Prevents pathological queries.
  **No upstream equivalent**; pure Giant Swarm addition. Upstream ships
  `get_query_examples` (hardcoded per-datasource snippets) which is a
  different thing — better modelled as a prompt in our `query-builder` slot.

Each ~50 LOC, reuses existing tools (`query_loki_stats`, `query_traces`,
`list_prometheus_metric_names`, etc.) internally.

## Tier 2 — production maturity

### Config validators (spun out of the transports split)

Paired with the transport work above but small enough to land separately.
Port from `mcp-kubernetes`:

- `validateSecureURL` — Grafana / Dex / OAuth-issuer URLs must be HTTPS
  unless `MCP_OAUTH_ALLOW_INSECURE_HTTP=true` is set.
- `validateOAuthClientID` — reject empty / whitespace-only client IDs.
- `validateTrustedSchemes` — constrain redirect URI schemes.
- Entropy check on `MCP_OAUTH_ENCRYPTION_KEY` — reject keys with obvious low
  entropy (repeats, ASCII sequences) before mcp-oauth burns a pod-start on
  token-decrypt failures.

Verify: table tests on each validator; `runServe` rejects bad values with a
clear error before opening any sockets.

### Per-org Grafana SA tokens — phase-2 blast-radius fix

**Biggest unresolved security concern in the current design.** Today one
compromised MCP pod with server-admin SA exposes every Grafana org. Fix
requires `observability-operator` coordination:

- Operator provisions per-`GrafanaOrganization` SAs and writes each to a
  namespaced Secret.
- MCP resolver picks the right SA per-request based on `OrgAccess.OrgID`.
- `GRAFANA_SA_TOKEN` / `GRAFANA_BASIC_AUTH` server-admin fallback remains as
  bootstrap path, documented as "dev/bootstrap only; never production".

### Write tools gated on Editor / Admin

The authz model (`authz.Role` with Editor/Admin, `GrafanaOrganization.spec.rbac.editors/admins`)
already supports this; `middleware.Audit` already emits the records
compliance will demand for writes.

Highest-value writes, matching upstream `grafana/mcp-grafana`'s surface:

- `create_silence(org, matchers, duration, comment)` — most-asked feature
  from SRE. "Silence this for 2 hours while I fix it." Gated on Editor.
  Upstream handles this via its combined `alerting_manage_rules` verb —
  we split it into discrete `create_silence` / (future) `delete_silence`
  for clearer MCP annotations (`destructiveHint: true` on each).
- `add_annotation(org, dashboardUid?, text, tags[])` — "mark this on the
  dashboard." Good for bot-driven deploy annotations. Matches upstream's
  `create_annotation`.
- `update_annotation(org, id, ...)` — matches upstream's `update_annotation`.
  Partial-update shape.

Every write carries `destructiveHint: true` in MCP annotations and rich
audit records (the `args` field captures the full payload; operators can
`jq` the audit stream for forensics).

Files: new `internal/tools/{writes,silences,annotations}.go`; handler
edits add role-gating via `d.Resolver.Require(ctx, caller, org, RoleEditor)`
before any write reaches Grafana / Alertmanager.

### Small gap-fills

Low-effort additions noted while comparing against upstream:

- `get_annotation_tags` — `/api/annotations/tags`, list tags for
  discovery. Matches upstream.
- `get_silence(org, uuid)` — single-silence read companion to
  `list_silences`, mirrors the `list_alerts` / `get_alert` pattern. No
  upstream equivalent (they don't split alerting-read tools).

### OTLP logs via `otelslog`

Currently logs go to stderr via `slog`. Wiring
`go.opentelemetry.io/contrib/bridges/otelslog` + `otlploghttp` onto the same
`OTEL_EXPORTER_OTLP_ENDPOINT` as traces gives free `trace_id` / `span_id`
correlation on every log record and unifies all three signals onto one
pipeline. Lives in `internal/observability/logging.go`.

## Tier 3 — features beyond the original plan

### Write tools gated on Editor / Admin

The authz model (`authz.Role` with Editor/Admin, `GrafanaOrganization.spec.rbac.editors/admins`)
already supports this. Highest-value writes:

- `create_silence(org, matchers, duration, comment)` — most-asked feature.
  "Silence this for 2 hours while I fix it." Gated on Editor.
- `add_annotation(org, dashboardUid?, text, tags[])` — "mark this on the
  dashboard." Good for bot-driven deploy annotations.

Each write carries `destructiveHint: true` in MCP annotations and rich audit
records for forensics.

### MCP resource subscriptions for firing alerts

Earlier we dropped resources in favour of tools (LLMs handle tools better).
But subscriptions are a push model tools can't do: subscribe to "firing
critical alerts in org X" → MCP pushes updates mid-conversation. Worth
revisiting once MCP clients broadly support resource subscriptions.

## Test-coverage gaps (ship when convenient)

- `internal/tools/dashboards.go#expandGrafanaVars` — substring-replacement for
  Grafana template macros (`$__rate_interval`, dashboard vars). Bug was fixed
  by sorting vars length-DESC so `$cluster` can't corrupt `$cluster_id`;
  guard with a table test.
- `internal/tools/dashboards.go#readJSONPointer` — our RFC 6901 implementation.
  Edge cases (`~0` / `~1` escapes, array indexing, non-container traversal)
  deserve coverage.

## Deferred from landed PRs

- **Helm PDB smart default** (from #5) — gate
  `templates/poddisruptionbudget.yaml` on `replicas > 1` and enable by
  default. Safe on single-replica (no-op template), automatic protection
  once scaled out.
- **ExternalSecret template** (from #5) — Dex creds via ESO. Postponed;
  mixing ESO with the existing `existingSecret` pattern cleanly is its own
  design call.
- **Auto-release on main merge** (from #6) — replace manual
  `release#vX.Y.Z` flow with an `auto-release.yaml` that patch-bumps on
  merge. Needs (a) skip-if-`[Unreleased]`-empty guard, (b) CHANGELOG
  promotion back to main with a bot token, (c) concurrency-serialize.
- **Standalone Go binaries via goreleaser** (from #6) — useful once local
  stdio deployments become a supported use case. Today the container image
  is the only distribution.
- **`ARCHITECTURE.md`** (from #4) — onboarding doc with hex diagram,
  "where to add X" cheat sheet, threat model. README's Layout section
  covers most of this; a standalone doc would formalize.

## Out of scope (explicitly not doing)

- **Multi-cluster / federating MCP.** One MCP per Grafana is a design
  constraint. A federator above it complicates auth, error propagation, and
  observability for marginal benefit. Let the LLM pick the right MCP per
  question.
- **Generic (non-Grafana) Prometheus/Loki/Tempo clients.** The current model
  leverages Grafana's tenant-header injection via its datasource proxy;
  bypassing it re-implements multi-tenancy.
- **Custom error-envelope format.** MCP's `isError: true` + plain text is
  what LLMs are trained on. Inventing a new schema is dead weight.
- **Result caching beyond the 30 s resolver cache.** Invalidation complexity
  outweighs the savings.
- **SBOM / cosign / CodeQL in-tree.** Handled at Giant Swarm org level, not
  per-repo, per existing convention.
- **Upstream feature parity** with Pyroscope / OnCall / Incident / Sift /
  Asserts in `grafana/mcp-grafana`. Keep the surface focused on Giant Swarm's
  stack.

## Upstream contribution lane

Parallel, non-blocking. Tracked in
[`docs/upstream-contributions.md`](./upstream-contributions.md):

- **US-1** — Per-request `X-Grafana-User` / `X-Grafana-Org-Id` pass-through.
- **US-2** — Mimir + Loki recording-rule tools (unblocked by our recording-
  rule PR above).
- **US-3** — Dedicated Tempo toolset.

### Candidates to propose back to `mcp-kubernetes`

Shared patterns worth upstreaming to the sibling MCP:

- Response-size cap helper + structured `response_too_large` payload.
- `datasourceProxyHandler` + `datasourceSpec` dispatch-table pattern.
- `paginateStrings` list-of-strings pagination helper.
- Typed `Role` enum with `MarshalJSON`.
- Controller-runtime-informer-backed authz resolver.
- Three-bucket `outcome` metric label + shared `Classify()` across middlewares.
