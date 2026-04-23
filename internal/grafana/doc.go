// Package grafana is a thin HTTP client for the Grafana API used by this MCP.
//
// It assumes the caller provides a Grafana server-admin service-account token
// (an SA granted the "Grafana Admin" server role), so that X-Grafana-Org-Id
// can switch org context per request. Regular org-scoped SAs will NOT work.
//
// # SSRF posture
//
// Every outbound request flows through [client.fetch], the package's single
// HTTP entry point. fetch builds the URL from c.base.JoinPath locally and
// never accepts a caller-constructed URL — so it is structurally impossible
// to point an API call at a host other than the configured Grafana base. The
// Authorization-bearing request cannot be sent off-origin from inside this
// package. Reinforced at runtime by [sameOriginRedirectPolicy] (same-host
// redirect budget; cross-origin redirects rejected) and, for the datasource
// proxy specifically, [validateDatasourceProxyPath] (traversal / leading-
// slash / length cap).
package grafana
