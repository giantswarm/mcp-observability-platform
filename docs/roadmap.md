# Productionize mcp-observability-platform — PR Plan

## Repo starting state (2026-04-20)

This repo is currently a **chart-only `devctl-app-template` scaffold** — a
stub Helm chart (`helm/mcp-observability-platform/`, staged in
working tree), CircleCI `architect` orb for app-catalog chart publishing,
two `zz_generated` GitHub workflows (team labels, project board), and
standard GS repo hygiene files (CHANGELOG, CODEOWNERS, DCO, LICENSE,
SECURITY, renovate). No Go code.

A **working Go prototype** lives at `~/mcp-observability-platform` (outside
this repo): 32 MCP tools, ~6k LOC, Grafana + Mimir/Loki/Tempo/Alertmanager
coverage, OIDC→`GrafanaOrganization` CR resolver, in-process OAuth
provider, working Helm chart with Prometheus metrics + OTel tracing.

Goal of this roadmap: **port the prototype into this repo and productionize
it** — matching the `giantswarm/mcp-kubernetes` shape (Go service + Helm
chart in one repo, CircleCI for chart publishing, GitHub Actions
`ci.yaml` / `auto-release.yaml` for code, devctl-generated workflows for
scanning/release hygiene).

## Decisions (resolved 2026-04-20)

1. **CI split**: follow the GS paved road — CircleCI + GitHub Actions
   together, matching `mcp-kubernetes`. CircleCI (via the `architect`
   orb) handles the GS-native parts: multi-arch container image push to
   `gsoci` + all registries, chart publish to `giantswarm-catalog`, ATS
   chart tests, Go binary build as input to the image. GitHub Actions
   handles Go code lint/test + the devctl-generated release/scanning
   hygiene workflows. PR 3 **expands** the existing
   `.circleci/config.yml` to the full 4-job mcp-kubernetes shape
   (`go-build`, `push-to-registries-multiarch`, `push-to-app-catalog`,
   `run-tests-with-ats`).
2. **Release flow**: explicit `release#vX.Y.Z` branches only. Push a
   release branch → `create_release_pr.yaml` opens a promotion PR that
   rewrites `## [Unreleased]` → `## [vX.Y.Z] - YYYY-MM-DD` in CHANGELOG
   → merge → `create_release.yaml` tags → CircleCI `architect` jobs pick
   up the tag and publish image + chart. No auto-release for v1. No
   goreleaser → GitHub Releases (in-cluster deployment is the primary
   distribution; standalone binaries for local stdio deferred). Both
   can be added later without rework.
3. **Prototype port**: single big PR 0 — pure import from
   `~/mcp-observability-platform`. Clean review boundary; subsequent PRs
   refactor a stable baseline.
4. **Go module path**: `github.com/giantswarm/mcp-observability-platform`.

## Strategy

Productionize what the prototype provides — don't expand tool surface
beyond small gaps (Loki recording rules, `get_silence`). Bootstrap
`mcp-kubernetes`-style CI, add one middleware seam for RBAC/audit/errors,
wire MCP protocol fundamentals (annotations, progress, cancellation,
prompts), harden ops. Upstream feature parity (Pyroscope/OnCall/Incident/
Sift) is explicitly out of scope. Contributions back to
`grafana/mcp-grafana` run in parallel for GS-native work worth sharing.

Target: **1 port PR + 10 productionization PRs in 3 waves**, each <500
LOC (except PR 0), independently reviewable. Tests live in the PR that
adds the feature. Helm chart work is consolidated into one PR; feature
PRs only read values seeded there.

## PR 0 — Port the Go prototype (code import only) — LANDED as PR #3 (scaffold) + PR #10 (tools)

Ported as two stacked PRs: #3 brought the scaffold (no tools / no chart)
and #10 brought the full MCP tool surface on top. Both merged to main.

- *Copy from prototype*: `main.go`, `cmd/`, `internal/` (authz, grafana,
  observability, server, tracing), `go.mod`, `go.sum`, `Dockerfile`,
  `Makefile`, prototype `README.md` content merged into this repo's
  README.
- *Adjust module path*: `go.mod` → `github.com/giantswarm/mcp-observability-platform`,
  update all imports.
- *Preserve the existing chart scaffold* (already staged in this repo) —
  PR 2 restructures it to match the prototype's chart, not a fresh write.
- *Carry over existing unit tests*.
- Verify: `go build ./...`, `go test ./...`, `helm lint helm/mcp-observability-platform`
  all green; repo runs via `go run . serve` with env vars matching
  prototype's config.

## MCP-spec alignment (narrow — deliberate)

Folded into PR 1-10 below. Kept narrow — MCP's spec is small and we don't
need to over-engineer error payloads, resources, or completions:

| Spec feature | Status today | Addressed in |
| --- | --- | --- |
| Tool annotations (`readOnlyHint`, `idempotentHint`, `openWorldHint`) | Likely absent after PR 0 — verify | PR 1 |
| `isError: true` with clear, LLM-actionable text | Ad hoc `NewToolResultError` | PR 5 (thin `mcperr`) |
| Progress notifications for long-running tools | None | PR 6 |
| Cancellation (`notifications/cancelled`) | None | PR 6 |
| Prompts (reusable templates) | None | PR 10 |
| Token-efficient responses (projections + response caps) | Already in prototype | existing + PR 1 |
| Paginated list tools | Already solid via `paginateStrings` | existing — no change |
| OAuth 2.1 + PKCE | Via `mcp-oauth` lib | existing, hardened in PR 9 |

**Not addressing** (deliberate): MCP logging (`logging/setLevel`) — slog
level at startup is enough; resources — duplicative of `list_orgs`;
completions — clients don't rely on them; sampling, elicitation — niche.

## Target architecture (after all PRs)

```
cmd/
  serve.go, serve_config.go, serve_stdio.go, serve_sse.go, serve_http.go   # split in PR 7
internal/
  authz/                # stays: OAuth, Dex, GrafanaOrganization resolver
  audit/                # NEW (PR 5): structured audit trail
  identity/             # LANDED (PR 1): caller identity plumbing
  mcperr/               # NEW (PR 5): thin helper — classification internal only, wire payload is plain isError+text
  mcpprogress/          # NEW (PR 6): progress + cancellation plumbing
  observability/        # LANDED (PR 1): Prometheus metrics + OTEL tracing (+ otelslog bridge planned)
  ratelimit/            # NEW (PR 9)
  server/
    server.go           # mcp-go + HTTP wiring
    health.go           # NEW (PR 4): /healthz + /readyz + /healthz/detailed
    middleware/         # LANDED (PR 1): Tracing + Metrics (tool-handler middleware)
    httpmiddleware/     # NEW (PR 9): security headers, CORS, HTTP metrics, rate limit
  tools/                # LANDED (PR 1): 32 tool handlers + shared helpers + annotations
helm/
  mcp-observability-platform/  # restructured + expanded in PR 2
```

Cross-cutting tool-call concerns live in `internal/server/middleware/` and
are registered once via `mcpsrv.WithToolHandlerMiddleware`. Adding Audit,
Progress, RateLimit means one more middleware + one more `Use()` call —
tool registrations stay untouched.

## Deferred follow-up (out of scope for the PRs above)

**OTLP logs via `otelslog`** — today logs emit to stderr via `slog`. Wiring
`go.opentelemetry.io/contrib/bridges/otelslog` + `otlploghttp` onto the
same `OTEL_EXPORTER_OTLP_ENDPOINT` as traces would give free
`trace_id`/`span_id` correlation on every log record and unify all three
signals onto one pipeline. Lives in `internal/observability/logging.go`
alongside `metrics.go` + `tracing.go`.

**Test-coverage gaps** (low-risk, ship when convenient):
- `internal/tools/dashboards.go#expandGrafanaVars` — substring-replacement
  logic for Grafana template macros (`$__rate_interval`, dashboard vars).
  Previous bug was fixed by sorting vars length-DESC to avoid `$cluster`
  corrupting `$cluster_id`; guard with a table test.
- `internal/tools/dashboards.go#readJSONPointer` — our RFC 6901
  implementation. Edge cases (escape sequences `~0`/`~1`, array indexing,
  non-container traversal) deserve coverage.

**`ARCHITECTURE.md`** — onboarding doc with the hex-arch dependency
diagram, "where to add X" cheat sheet, and threat model. Extract from the
review in PR #4 when the dust settles.

**Helm PDB smart default** — gate
`helm/mcp-observability-platform/templates/poddisruptionbudget.yaml` on
`replicas > 1` and enable it by default. Safe on single-replica deploys
(template renders nothing) + automatic protection once operators scale
out. Small addition to PR #2's values.yaml.

**Propagate pre-commit Go tools upstream** — `giantswarm/github` PR #5098
adds the Go toolchain install to the central
`zz_generated.pre-commit.yaml`. Once merged, this repo's local copy can
be regenerated.

**`mcp-oauth` session lifecycle adoption** — mcp-oauth v0.2.102 added
`SessionIDFromContext`, `SetTokenRefreshHandler`,
`SetSessionCreationHandler`, `SetSessionRevocationHandler`. These unlock:
- Per-session state cleanup on logout (PR 9 rate-limit state, PR 8 audit
  session correlation).
- Active OAuth token refresh with a callback for downstream cache
  invalidation (PR 9).
- Session-scoped identity in `internal/identity/` — expose
  `CallerSessionID(ctx)` once audit / progress PRs need it.

## The 10 productionization PRs

Each: branch, <500 LOC diff, single concern, green in CI.

### Wave 0 — Foundation

**PR 1 · Tool middleware + MCP annotations + package restructure — LANDED in `pr-1-wrap-middleware` (#4)**

Replaced the in-package `instrument()` wrapper with mcp-go's built-in
`server.ToolHandlerMiddleware` mechanism. Two runtime middlewares applied
globally via `WithToolHandlerMiddleware`: `middleware.Tracing()` (OTEL span
per call) and `middleware.Metrics()` (counter + histogram). mcp-go's
`WithRecovery()` is wired too — panic safety we never had before.

MCP tool annotations surface on every tool via `tools.ReadOnlyAnnotation()`:
`readOnlyHint: true`, `openWorldHint: true`, `destructiveHint: false`.
`idempotentHint` deliberately omitted — many tools (`query_metrics`,
`query_logs`, `list_alerts`) return live data that changes between calls.

Metric label `outcome` is 3-bucket (`ok` / `user_error` / `system_error`)
so operators distinguish real incidents from expected user-visible failures
(missing args, authz denials). Span `tool.outcome` attribute mirrors this.

Package restructure landed alongside:
- `internal/tracing/` merged into `internal/observability/`.
- `internal/server/tools_*.go` (8 files) → `internal/tools/*.go` (package `tools`).
- Caller identity plumbing → `internal/identity/`.
- Tool middlewares → `internal/server/middleware/`.
- MCP resources / prompts stubs stay in `internal/server/` (they're peer
  MCP surfaces, not tools).

Dependencies upgraded: `mark3labs/mcp-go` → v0.49.0 (the version that
introduced `ToolHandlerMiddleware`, `WithRecovery`, `NewToolResultErrorf`),
Go toolchain → 1.25.5.

**PR 2 · Helm chart productionization (all-in-one) — LANDED in `pr-2-helm-hardening` (#5)**
Single Helm PR — everything chart-related landed here. Scope shipped:
- Templates: Chart.yaml (audience/managed/team annotations), `_helpers.tpl`,
  Deployment (with `checksum/config` rollout on ConfigMap changes, envFrom
  runtime ConfigMap), Service, ServiceAccount, ClusterRole+Binding,
  ServiceMonitor, PodDisruptionBudget, NetworkPolicy (ingress + optional
  egress with auto-included kube-dns allow), HorizontalPodAutoscaler,
  VerticalPodAutoscaler, runtime ConfigMap, NOTES.txt.
- Runtime ConfigMap exposes `MCP_TOOL_TIMEOUT`, `TOOL_MAX_RESPONSE_BYTES`
  (0 disables the cap), `MCP_RESOLVER_CACHE_TTL`,
  `MCP_RATE_LIMIT_{PER_CALLER,PER_ORG,GLOBAL}`, `MCP_OAUTH_REFRESH_AHEAD`.
  Feature PRs (8, 9) read these without chart changes.
- `values.schema.json` regenerated via `helm-values-schema-json` v2.3.1
  (the binary the pre-commit workflow installs).
- helm-unittest specs: `tests/{configmap,deployment,hpa,networkpolicy,pdb,
  servicemonitor,vpa}_test.yaml` (19 tests, all green).
- Example overlays: `values-memory.yaml` (dev), `values-valkey.yaml` (prod
  OAuth store), `values-rbac-minimal.yaml` (external SA), and
  `values-autoscaling.yaml` (HPA + VPA Initial + PDB + NetworkPolicy egress).
- `README.md.gotmpl` for helm-docs generation.

**Deferred**: `externalsecret.yaml` (Dex creds via ESO) — postponed to a
follow-up because mixing ESO with the existing `existingSecret` pattern
cleanly is its own design call. `values.schema.yaml` (human-readable
source for the generator) — the JSON is hand-edited for now and regen is
a one-liner.

**PR 3 · CI — match `giantswarm/mcp-kubernetes` full shape**
Expand CircleCI to full 4-job mcp-kubernetes shape + add GitHub Actions
Go-code layer + devctl-generated release/scanning workflows.

- *Expand `.circleci/config.yml`*: upgrade `giantswarm/architect` orb to
  the version used by mcp-kubernetes and add jobs beyond the current
  single chart-publish: `architect/go-build` (binary name:
  `mcp-observability-platform`), `architect/push-to-registries-multiarch`
  (amd64 on branches, multi-arch on `/^v.*/` tags, publishing to
  `gsoci.azurecr.io` + all registries including China mirrors),
  `architect/run-tests-with-ats` (chart ATS tests). Keep existing
  `architect/push-to-app-catalog` — it stays as-is.
- *Hand-written GitHub workflows*:
  - `.github/workflows/ci.yaml` — PR + push-main: `actions/checkout@v6`
    → `actions/setup-go@v5` (Go 1.26, `cache: true`) →
    `actions/setup-python@v6` (yamllint) → `azure/setup-helm@v5`
    (Helm v3.19.4) → `helm plugin install helm-unittest` →
    `make check helm-lint helm-test govulncheck`.
  - **No `auto-release.yaml`** — deferred (see Future Work).
- *Generated via `giantswarm/devctl gen workflows`* (expand beyond the
  two existing ones): `zz_generated.{create_release, create_release_pr,
  check_values_schema, pre-commit, gitleaks, run_ossf_scorecard,
  fix_vulnerabilities, validate_changelog}.yaml`. All reference
  `giantswarm/github-workflows/.github/workflows/*.yaml@main`.
- *Repo-level config*: `.pre-commit-config.yaml`
  (`detailyang/pre-commit-shell@v1.0.5`,
  `pre-commit/pre-commit-hooks@v6.0.0`,
  `dnephin/pre-commit-golang@v0.5.1`), extend existing `renovate.json5`
  to include `:lang-go.json5`, `Makefile.custom.mk` (`check`, `test-vet`,
  `helm-lint`, `helm-test`, `govulncheck`), review/confirm `Dockerfile`
  (multi-stage `golang:1.26.x` → `scratch`, `giantswarm` user — needed
  by `architect/push-to-registries-multiarch`). **No `.goreleaser.yaml`**
  — binary distribution happens via the container image, standalone
  binaries deferred (see Future Work).
- *Explicitly NOT doing* (matches GS pattern): cosign, SBOM in-tree,
  CodeQL, Dependabot, Codecov, chart-releaser, goreleaser, auto-release.
- Release flow: push `release#vX.Y.Z` branch → `create_release_pr.yaml`
  opens CHANGELOG-promotion PR → merge → `create_release.yaml` tags →
  CircleCI `architect` jobs publish image + chart on the `v*` tag.
- Verify: `ci.yaml` green on a no-op PR; `pre-commit run --all-files`
  passes; `make helm-test` green; first `release#v0.1.0` branch opens a
  promotion PR; merging it produces a tag that CircleCI picks up and
  pushes both image and chart.

**PR 4 · Deep readiness + `/healthz/detailed` + two-phase shutdown — LANDED in `pr-4-readiness` (PR #7)**

Replaced the always-200 `/healthz` and `/readyz` stubs with proper
liveness vs readiness semantics, added a JSON `/healthz/detailed`
endpoint, and reordered graceful shutdown so in-flight tool calls aren't
SIGKILLed mid-drain.

`internal/server/health.go` introduces `HealthChecker` with
`Register(name, CheckFn)` plus three handlers:
- `/healthz` — liveness, always 200 unless the process itself is dead
  (does NOT run readiness probes — a flaky downstream should not
  restart the pod).
- `/readyz` — 503 when any probe fails. Probes run concurrently under
  a shared 2s deadline.
- `/healthz/detailed` — JSON with per-check status, duration, overall
  status, uptime, version, probe-specific extras.

Three probes wired in `cmd/serve.go`:
- `grafana` — new lightweight `grafana.Client.Ping()` against
  `/api/health` (auth-free; cheaper than `VerifyServerAdmin`, which
  lists all orgs on every probe).
- `dex` — `HTTPProbe` against the Dex OIDC discovery endpoint.
- `k8s_cache` — `ctrlCache.List(GrafanaOrganizationList)`; reports
  `{orgs: N}` under `extra`.

**Two-phase shutdown**: drain MCP first (10s) while the observability
server keeps answering liveness + Prometheus scrapes, then drain
observability (5s). Prevents the kubelet from interpreting a slow
tool-call drain as a dead pod and firing SIGKILL.

Helm chart probes at `/healthz` and `/readyz` (already in place from
PR 2) now gate rollouts on real state — no chart changes needed.

### Wave 1 — Ship-ready + MCP fit

**PR 5 · `internal/audit` + thin `mcperr`**
Both hang off `server/middleware`.
- *Audit*: structured JSON record per tool call — `{caller, org, tool,
  args_redacted, outcome, duration_ms, error_class}` + counter metric.
- *Thin `mcperr`*: classification (`UserError`/`AuthzError`/
  `TransientError`/`SystemError`) stays **internal** — used only for
  audit enrichment + metric labels. **Wire payload is plain `isError:
  true` with clear, LLM-actionable text** (e.g. "metric name not
  found — try `list_prometheus_metric_names`"). No custom `retry_after`
  field; no structured error envelope. ~50 LOC helper.
- Verify: table tests per class; one audit record per tool call; errors
  render as plain `isError: true` text

**PR 6 · MCP progress + cancellation**
Long-running tools (`query_metrics` range, `query_logs`, `get_panel_image`)
emit `notifications/progress`. MCP `notifications/cancelled` propagates
into Grafana HTTP calls via context cancellation.
- Files: new `internal/mcpprogress/mcpprogress.go`; edit `internal/server/middleware/wrap.go`, handlers in `internal/tools/metrics.go`, `logs.go`, `panels.go`
- Verify: component test issues a query then cancels mid-flight; progress events received

**PR 7 · Refactor — `cmd/serve.go` split + implement `--transport` + config validators**
- *Split `cmd/serve.go`* (440 LOC) into `cmd/{serve.go, serve_config.go,
  serve_stdio.go, serve_sse.go, serve_http.go}` matching
  `mcp-kubernetes`. OAuth wiring → `internal/authz/oauth_setup.go`,
  K8s informer → `internal/authz/k8s_setup.go`, HTTP mux →
  `internal/server/httpmux.go`.
- *Implement `--transport`*: currently accepted but ignored (code always
  serves streamable-http). Wire `stdio` and `sse` branches.
- *Config validators*: port `validateSecureURL`, `validateOAuthClientID`,
  `validateTrustedSchemes`, entropy check on `MCP_OAUTH_ENCRYPTION_KEY`
  from mcp-kubernetes.
- *Split hot spots* (code movement only): `tools_dashboards.go` (1011
  LOC) → list/summary/queries/render; extract histogram cardinality from
  `tools_metrics.go` (538 LOC).
- Verify: `go test ./...` unchanged; `--transport=stdio` actually serves
  stdio; validation fails on bad URL/entropy.

### Wave 2 — GS differentiation + hardening

**PR 8 · Mimir + Loki recording rules + `get_silence`**
Three gaps closed together.
- *Mimir recording rules* (matches prototype branch
  `fix-cluster-recording-rules`): `list_mimir_recording_rules`,
  `get_mimir_recording_rule`.
- *Loki recording rules* (no existing tooling): `list_loki_recording_rules`,
  `get_loki_recording_rule` via Loki ruler API (`/loki/api/v1/rules`).
- *`get_silence` companion*: `list_silences` already exists in prototype;
  add `get_silence(org, uuid)` matching the `list_alerts`/`get_alert`
  pattern.
- Candidates for upstream (US-2 in upstream-contributions.md).
- Verify: unit tests with httptest Mimir + Loki ruler stubs.

**PR 9 · HTTP middleware chain + rate limit + OAuth token refresh**
Go code only — reads ConfigMap fields seeded in PR 2. No Helm changes.
- *HTTP middleware chain*: `SecurityHeaders` → `CORS` → `HTTPMetrics` →
  existing `oauthHandler.ValidateToken` → MCP server.
- *Rate limiting*: per-caller + per-org + global token bucket, thresholds
  from ConfigMap, rejections render `isError: true` with rate-limit text.
- *OAuth token refresh*: active refresh before expiry; refresh failure →
  auth error prompting re-auth.
- Files: new `internal/server/middleware/{security,cors,metrics,ratelimit}.go`;
  new `internal/ratelimit/ratelimit.go`; edit
  `internal/authz/resolver.go`, `internal/authz/oauth_setup.go`,
  `internal/server/httpmux.go`.
- Verify: OWASP headers present; load test caps enforced; token-expiry
  table test with clock fake; CORS preflight to `/mcp` works from a
  browser origin.

**PR 10 · MCP prompts — runbook templates**
Upstream `grafana/mcp-grafana` has none — pure differentiator.
Parameterized templates chaining existing tools: investigate a firing
alert, tenant cardinality audit, dashboard health check.
- Files: new `internal/server/prompts.go`
- Verify: `prompts/list` returns expected names; one prompt renders
  deterministically in a test

## Upstream contribution lane (parallel, non-blocking)

See [`upstream-contributions.md`](./upstream-contributions.md) for
US-1/2/3.

## Cross-repo contribution candidates (`mcp-kubernetes`)

Patterns worth proposing back: response-size cap helper; datasource
proxy-handler dispatch-table pattern; `paginateStrings` helper; typed
`Role` enum with `MarshalJSON`; CR-backed authorization via
controller-runtime informer.

## Verification strategy

- **Per PR**: in-package unit/component tests + `go test -race ./...`
  green in CI (from PR 3 onward).
- **PR 3 exit**: `ci.yaml`, `pre-commit`, `helm-unittest`,
  `goreleaser --snapshot` all green; first tag auto-publishes Go
  artifacts; CircleCI publishes chart.
- **Wave 1 exit**: error classes observable in logs + metrics;
  cancellation works end-to-end.
- **Wave 2 exit**: `helm install` end-to-end in kind (probes green,
  prompt renders, tool call succeeds); multi-org RBAC scenario test
  green.
- **Manual smoke (auth-path PRs)**: run Claude Desktop or `mcp-cli`
  against a local deploy, exercise 2-3 tools across categories.

## Sequencing

PR 0 (port) → Wave 0 (PRs 1-4) → Wave 1 (PRs 5-7) → Wave 2 (PRs 8-10).
Waves sequential; within a wave, PRs parallelize across reviewers.

## Future work — out of scope

- **Write operations** (silences create/delete, annotations create/
  update, dashboard patch, incident create). Architecture supports
  extension: gate via existing `Role` enum (`RoleEditor`/`RoleAdmin` on
  `OrgAccess`), wrap through `server/middleware` for audit, new
  `tools_*_write.go` files, MCP `destructiveHint: true` on every write
  tool.
- **Upstream feature parity** (Pyroscope, OnCall, Incident, Sift,
  Asserts) via adapter pattern. Deferred to keep v1 focused.
- **Auto-release on main merge**: replace the manual `release#vX.Y.Z`
  branch flow with an `auto-release.yaml` that bumps patch on every
  main merge. Would need to (a) guard with "skip if `[Unreleased]` is
  empty", (b) promote CHANGELOG + commit back to main with a bot token,
  (c) concurrency-serialize to avoid racing pushes. Minor/major bumps
  would stay on the explicit release-branch flow.
- **Standalone Go binary releases** via goreleaser → GitHub Releases
  (`.goreleaser.yaml` + `auto-release.yaml` / `release.yaml`). Useful
  once local stdio MCP deployments become a supported use case —
  matches what `mcp-kubernetes` does today. For v1, the container
  image + Helm chart is the only supported distribution.
