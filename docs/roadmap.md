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

### 5. Unified rule listing (Mimir + Loki, alerting + recording)

Today's `alerting_manage_rules` (delegated, Mimir-bound) is broken end-to-end
on the Giant Swarm setup. Three independent gaps stack:

1. **Path doubling against the Mimir gateway.** observability-operator
   provisions Grafana datasources with URL `http://mimir-gateway.mimir.svc/prometheus`
   (the `/prometheus` is in the URL). Upstream
   `mcp-grafana@v0.12.1/tools/alerting_manage_rules_datasource.go` hardcodes
   `prometheus/api/v1/rules` on the path, so the proxied request becomes
   `/prometheus/prometheus/api/v1/rules` — no nginx location matches → 404.
   The right path against these datasources is `api/v1/rules` (relative to
   the DS URL root). Mimir gateway nginx itself is fine — it routes
   `/prometheus/api/v1/rules` to the ruler.
   - **Fix**: upstream PR adding a config knob (or stripping the redundant
     `prometheus/` when it's already in the DS URL). Confirmed against
     graveler 2026-04-29: hitting `gs-mimir-giantswarm` with `api/v1/rules`
     directly returns 665 rule groups; with `prometheus/api/v1/rules`
     returns 404.

2. **Binder picks the wrong Mimir DS.** `MatchKind("mimir")` returns the
   first substring match, which on graveler is the multi-tenant `GS Mimir`
   (id=2). The mono-tenant rulers (`GS Mimir (giantswarm)` id=18, `GS Mimir
   (notempty)` id=21) are the ones that actually answer rule queries
   without ambiguity, but the binder never reaches them. Multi-tenant
   `gs-mimir` returns `400 no valid org id found` because the per-DS
   X-Scope-OrgID isn't set for the gateway-routed ruler path.
   - **Fix**: read-only fanout. `MatchKindAll(dss, kind)` returns every
     mono-tenant ruler the org has access to; `alerting_manage_rules`
     calls each and merges results, tagging each rule with its source
     `datasource_uid` so the LLM can disambiguate. If only one mono-tenant
     DS exists, hit just that one. Caller-supplied `datasource_uid` is the
     escape hatch. Write ops (silences, future create/delete) must NOT
     fanout — they pick a single tenant.

3. **Recording rules dropped at upstream's projection.**
   `mcp-grafana@v0.12.1/tools/alerting_manage_rules_datasource.go:80-82`
   hits `case v1.RecordingRule: continue`. The `alertRuleSummary`
   projection already carries `Name`, `RuleGroup`, `Labels`, `Health`,
   `LastEvaluation`; recording rules need only an empty
   `State`/`For`/`Annotations`. Two-line upstream change + projection-shape
   test.

After (1) and (3) land upstream and (2) lands locally, register
`alerting_manage_rules` a second time bound to `DSKindLoki` for the
mono-tenant Loki rulers (gateway path also `/prometheus/api/v1/rules`
through the same gateway pattern). Tool-name collision means we either
need an upstream-side rename to a kind-neutral name or a name-override
hook in our binder.

**Note: this does NOT subsume our local Alertmanager v2 tools.** Upstream
`mcp-grafana@v0.12.1` has no equivalent for `list_alerts`/`get_alert`
(active alert instances via `/api/v2/alerts`). Its alerting tools are
config-only (`alerting_manage_rules`, `alerting_manage_routing`,
contact-points). Dropping our local AM tools needs a separate upstream PR
adding AM v2 support, or stays out of scope.

Until 1+2+3 land, recording rules and Loki rules are not exposed;
operators query the Grafana UI or the ruler endpoints directly. Smoke
test from a Grafana pod for a mono-tenant Mimir DS:

```
curl -H "X-Grafana-Org-Id: 2" -u "admin:$PW" \
  http://localhost:3000/api/datasources/proxy/uid/gs-mimir-giantswarm/api/v1/rules
```

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
