// Package authz decides which Grafana orgs a caller may act on, and with
// what Role.
//
// Grafana is the source of truth. observability-operator writes an
// org_mapping string to Grafana's SSO settings, and Grafana itself evaluates
// that mapping at each user login to compute per-user (org -> role).
// This package asks Grafana "what orgs does caller X have, and in what role?"
// via /api/users/lookup + /api/users/{id}/orgs, then enriches each result
// with tenant metadata drawn from an OrgLister (an informer cache of
// GrafanaOrganization CRs in production, an in-memory stub in tests).
// Datasource resolution is not part of authz — tool handlers fetch the
// live datasource list directly via grafana.Client.ListDatasources.
//
// Falling back to CR RBAC evaluation on the MCP side would re-implement
// Grafana's semantics (group matching, "*" wildcard, precedence, casing) and
// drift over time. By deferring to Grafana we inherit whatever mapping logic
// Grafana ships today and whatever it ships tomorrow.
//
// # Layout
//
//   - authorizer.go — Authorizer + RequireOrg / ListOrgs.
//   - cache.go      — TTL'd per-caller role cache + clone discipline.
//   - role.go       — Role enum.
//   - caller.go     — Caller + OrgLister port + context helpers.
//   - types.go      — Organization + Tenant + TenantType domain types
//     plus HasTenantType. Tool-handler consumers import these, never the CRD.
//   - errors.go     — Sentinel errors.
package authz
