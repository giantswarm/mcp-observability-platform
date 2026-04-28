# Upstream contributions — `grafana/mcp-grafana`

Parallel, non-blocking lane tracked alongside the local [roadmap](./roadmap.md).
Each item is independently pitched upstream; none are blockers for local PRs.

## US-1 — Context-aware RoundTrippers

**Issue:** [grafana/mcp-grafana#794](https://github.com/grafana/mcp-grafana/issues/794)

Make the RoundTrippers built by `BuildTransport` (`OrgIDRoundTripper`,
`AuthRoundTripper`, `ExtraHeadersRoundTripper`) honour a per-request
`GrafanaConfig` overlay from `req.Context()` instead of freezing values
at client construction time.

**Why this framing replaces the original "per-request user/org headers"
proposal:** upstream already supports `GRAFANA_FORWARD_HEADERS` for the
gateway-injects-headers deployment model. The actual gap is for MCP
servers that terminate OAuth themselves — the identity exists only in
the validated request context, not in inbound HTTP headers, and
upstream's RoundTrippers don't read context per-call. Extending them is
small (one branch per RoundTripper) and benefits any multi-tenant
consumer.

**Local equivalent:** `internal/grafana/client.go` `RequestOpts{OrgID, Caller}`
threaded through every call.

## US-2 — Mimir + Loki recording-rule tools

**Issue:** [grafana/mcp-grafana#795](https://github.com/grafana/mcp-grafana/issues/795)

Add first-class tools for the Prometheus/Mimir and Loki ruler APIs:
`list_mimir_recording_rules`, `get_mimir_recording_rule`,
`list_loki_recording_rules`, `get_loki_recording_rule`. Recording rules
are a distinct shape from alerting rules (no `for` / Alertmanager
plumbing) and deserve their own list/filter tooling.

## US-3 — Dedicated Tempo toolset

**Issue:** [grafana/mcp-grafana#796](https://github.com/grafana/mcp-grafana/issues/796)

Upstream exposes Tempo only indirectly via Sift's `find_slow_requests`,
which requires a Grafana Cloud Sift backend. OSS users running Tempo
on their own infrastructure can't reach it from the MCP today.

The proposal: four read-only tools wrapping Tempo's existing HTTP API:
`query_traces` (TraceQL via `/api/search`), `query_tempo_metrics`
(`/api/metrics/query_range`), `list_tempo_tag_names`
(`/api/v2/search/tags`), `list_tempo_tag_values`
(`/api/v2/search/tag/{tag}/values`).

**Local equivalent:** `internal/tools/traces.go`.

## Contribution workflow

1. Open a GitHub issue upstream describing the gap + our local
   implementation. (Done for US-1/US-2/US-3.)
2. Wait for maintainer signal on shape/scope before opening a PR.
3. Keep the upstream change minimal — our GS-specific authz/audit layer
   stays local, not upstreamed.
4. Once merged upstream, drop any vendored copy locally.

## Candidates to propose back to `mcp-kubernetes`

Shared patterns worth upstreaming to the sibling MCP:

- Response-size cap helper + structured `response_too_large` payload.
- `paginateStrings` (and `Paginated[T]`).
- Typed `Role` enum with `MarshalJSON`.
- Controller-runtime-informer-backed authz resolver.
- `RequireCaller` middleware (fail-closed authentication on top of
  mcp-go's tool-handler middleware stack).
