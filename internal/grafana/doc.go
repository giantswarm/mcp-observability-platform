// Package grafana is the Grafana adapter for this MCP. It carries:
//
//   - A thin HTTP client (client.go) for operations not delegated to
//     upstream's per-call GrafanaClient: user / membership lookups
//     (LookupUser, UserOrgs); datasource UID lookup
//     (LookupDatasourceUIDByID); reachability probes (Ping,
//     VerifyServerAdmin); and a generic datasource passthrough
//     (DatasourceProxy).
//   - Datasource + DatasourceKind taxonomy (datasource.go) — the
//     domain projection of a Grafana datasource and its canonical-
//     role enum, with MatchKind for substring-based selection.
//
// The surface is deliberately minimal — most Grafana operations are
// delegated to upstream grafana/mcp-grafana, which builds its own HTTP
// client per call.
//
// It assumes the caller provides a Grafana server-admin service-account
// token (an SA granted the "Grafana Admin" server role), so that
// X-Grafana-Org-Id can switch org context per request. Regular
// org-scoped SAs will NOT work. BasicAuth on the built-in admin user
// is supported as a dev/bootstrap fallback when SA promotion isn't
// available — see Config.
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
