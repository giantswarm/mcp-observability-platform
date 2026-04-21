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

## PR 0 — Port the Go prototype (code import only)

One large PR, reviewed as a bulk import. No refactors — just get the
prototype into this repo's git history under the correct module path.

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
  mcperr/               # NEW (PR 5): thin helper — classification internal only, wire payload is plain isError+text
  mcpprogress/          # NEW (PR 6): progress + cancellation plumbing
  ratelimit/            # NEW (PR 9)
  server/
    health.go           # NEW (PR 4): /healthz + /readyz + /healthz/detailed
    middleware/         # NEW (PR 9): security headers, CORS, HTTP metrics, rate limit
  tools/wrap/           # NEW (PR 1): single middleware — RBAC + audit + X-Grafana-User + progress + timeout + response cap
helm/
  mcp-observability-platform/  # restructured + expanded in PR 2
```

One enforcement point (`wrap.Wrap`) means every cross-cutting concern
lives in one package.

## The 10 productionization PRs

Each: branch, <500 LOC diff, single concern, green in CI.

### Wave 0 — Foundation

**PR 1 · `tools/wrap` middleware + MCP tool annotations**
Extract `instrument()`, RBAC, response cap, arg extraction, timeout,
`X-Grafana-User` injection into `internal/tools/wrap`. Refactor the 8
`tools_*.go` files to register through it. Add MCP annotations
(`readOnlyHint: true`, `idempotentHint`, `openWorldHint: true`) on every
tool. No behavior change beyond annotations.
- Files: new `internal/tools/wrap/wrap.go`; edit all `internal/server/tools*.go` (8 files); edit `internal/server/tools.go`
- Verify: existing unit tests pass; `tools/list` response includes annotations

**PR 2 · Helm chart productionization (all-in-one)**
Single Helm PR — everything chart-related lands here. Covers restructure,
schema, tests, and all opt-in hardening templates. Feature PRs later read
these values; no later PR touches the chart.
- *Restructure*: merge prototype's richer `helm/mcp-observability-platform/`
  templates with this repo's scaffold. Chart name = repo name.
- *Schema + tests*: `values.schema.json` + `.schema.yaml`; helm-unittest
  specs under `helm/mcp-observability-platform/tests/`.
- *Runtime ConfigMap*: all tunables exposed upfront — timeouts, response
  cap, resolver cache TTL, rate-limit thresholds (per-caller/per-org/
  global), token-refresh ahead-of-expiry window. Feature PRs (8, 9) read
  these; fields start with safe no-op defaults until code consumes them.
- *Opt-in templates*: `configmap.yaml`, `externalsecret.yaml` (Dex creds
  pattern), `networkpolicy.yaml` with egress to Grafana + K8s API + Dex
  + OTLP only, `poddisruptionbudget.yaml`.
- *Autoscaling* (opt-in, default off): `hpa.yaml` (CPU 70% / memory 80%
  targets, configurable), `vpa.yaml` (`updatePolicy: Auto` default,
  configurable). Values gates: `hpa.enabled`, `vpa.enabled`.
- *Example overlays*: `values-valkey.yaml`, `values-memory.yaml`,
  `values-rbac-minimal.yaml`, `values-autoscaling.yaml`.
- *Docs*: `README.md.gotmpl` for helm-docs.
- Verify: `helm lint`; `helm unittest` green; `helm template -f values-valkey.yaml` produces expected manifests; kind deploy end-to-end with NetworkPolicy blocking external internet.

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

**PR 4 · Deep readiness + `/healthz/detailed` + two-phase shutdown**
Adopts the `mcp-kubernetes` pattern. Split `/healthz` (liveness, always
200) from `/readyz` (Grafana reachable, K8s informer synced, Dex OIDC
metadata resolvable; per-check 2s timeout). Add `/healthz/detailed` —
JSON with uptime, resolver cache size, Grafana probe RTT, Dex discovery
status. Two-phase graceful shutdown: drain metrics/health server first
(5s), then MCP server (10s).
- Files: new `internal/server/health.go`; edit `cmd/serve.go`
- Verify: kind `kubectl rollout status` blocks until Grafana is up;
  `curl /healthz/detailed` returns rich JSON; `kubectl delete pod`
  shows MCP drains before metrics

### Wave 1 — Ship-ready + MCP fit

**PR 5 · `internal/audit` + thin `mcperr`**
Both hang off `tools/wrap`.
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
- Files: new `internal/mcpprogress/mcpprogress.go`; edit `internal/tools/wrap/wrap.go`, handlers in `internal/server/tools_metrics.go`, `tools_logs.go`, `tools_panels.go`
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
  `OrgAccess`), wrap through `tools/wrap` for audit, new
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
