# Upstream contributions — `grafana/mcp-grafana`

Parallel, non-blocking lane tracked alongside the local [roadmap](./roadmap.md).
Each item is independently pitched upstream; none are blockers for local PRs.

## US-1 — First-class inbound per-request user/org headers

Propagate `X-Grafana-User` and `X-Grafana-Org-Id` from incoming MCP request
context into the Grafana client's per-request transport. Builds on upstream
v0.11.6's on-behalf-of header pass-through — extends it from auth-header-only
to full per-request identity + org attribution for multi-tenant deployments.

## US-2 — Mimir + Loki recording-rule tools

Add first-class tools for the Prometheus/Mimir and Loki ruler APIs:
`list_mimir_recording_rules`, `get_mimir_recording_rule`,
`list_loki_recording_rules`, `get_loki_recording_rule`. Recording rules are
a distinct shape from alerting rules (no `for` / alertmanager plumbing) and
deserve their own list/filter tooling.

**Blocked by**: local PR 8 landing.

## US-3 — Dedicated Tempo toolset

Upstream `grafana/mcp-grafana` exposes Tempo only indirectly via Sift's
`find_slow_requests`. Our local `tools_traces.go` + `tools_tempo_*` cover
`query_traces`, `query_tempo_metrics`, `list_tempo_tag_names`,
`list_tempo_tag_values` with TraceQL support. Polish and contribute.

**Blocked by**: nothing (code exists in prototype) — needs a contribution-
friendly diff against upstream's tool registration style once PR 0 lands
that code here.

## Contribution workflow

1. Open a GitHub issue upstream describing the gap + our local implementation.
2. Wait for maintainer signal on shape/scope before opening a PR.
3. Keep the upstream change minimal — our GS-specific authz/audit layer
   stays local, not upstreamed.
4. Once merged upstream, drop any vendored copy locally.
