// Package grafana is the Grafana adapter for this MCP. It carries:
//
//   - A thin HTTP client (client.go) covering what gfBinder (in
//     internal/tools) can't:
//     authz lookups (LookupUser, UserOrgs); ID→UID resolution
//     (LookupDatasourceUIDByID); health-probe Ping + startup
//     VerifyServerAdmin; DatasourceProxy for the local tools (Tempo,
//     Alertmanager v2).
//   - Datasource + DatasourceKind taxonomy (datasource.go) — the
//     domain projection of a Grafana datasource and its canonical-
//     role enum, with MatchKind for substring-based selection.
//
// The interface is deliberately minimal — most tools are delegated to
// upstream grafana/mcp-grafana, which builds its own HTTP client per
// call.
//
// It assumes the caller provides a Grafana server-admin service-account
// token (an SA granted the "Grafana Admin" server role), so that
// X-Grafana-Org-Id can switch org context per request. Regular
// org-scoped SAs will NOT work.
//
// # SSRF posture
//
// Every outbound request flows through [client.fetch], the package's
// single HTTP entry point. fetch builds the URL from c.base.JoinPath
// locally and never accepts a caller-constructed URL — so it is
// structurally impossible to point an API call at a host other than
// the configured Grafana base. The Authorization-bearing request
// cannot be sent off-origin from inside this package. For the
// datasource proxy specifically, [validateDatasourceProxyPath] guards
// against traversal / leading-slash / length-cap escapes in the
// caller-supplied path segment.
package grafana
