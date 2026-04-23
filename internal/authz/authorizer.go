// Package authz decides which Grafana orgs a caller may act on, and with
// what Role.
//
// Grafana is the source of truth. observability-operator writes an
// org_mapping string to Grafana's SSO settings, and Grafana itself evaluates
// that mapping at each user login to compute per-user (org -> role).
// This package asks Grafana "what orgs does caller X have, and in what role?"
// via /api/users/lookup + /api/users/{id}/orgs, then enriches each result
// with tenant/datasource metadata drawn from an OrgRegistry (an informer
// cache of GrafanaOrganization CRs in production, an in-memory stub in
// tests).
//
// Falling back to CR RBAC evaluation on the MCP side would re-implement
// Grafana's semantics (group matching, "*" wildcard, precedence, casing) and
// drift over time. By deferring to Grafana we inherit whatever mapping logic
// Grafana ships today and whatever it ships tomorrow.
//
// # Layout
//
//   - authorizer.go — Authorizer + RequireOrg/ListOrgs/load (this file).
//   - cache.go    — LRU + singleflight + TTL + clone discipline.
//   - role.go     — Role enum.
//   - caller.go   — Caller + OrgRegistry + OrgMembershipLookup ports.
//   - types.go    — Organization + Tenant + Datasource + TenantType domain
//     types plus the methods tool handlers call (HasTenantType,
//     FindDatasourceID). Tool-handler consumers import these, never the CRD.
//   - errors.go   — Sentinel errors.
package authz

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
)

// Authorizer decides whether a caller may act on a given Grafana org, and at
// what Role. It's the authz package's main consumer-facing surface: tool
// handlers hold one and call RequireOrg() before touching any datasource.
//
// The production implementation asks Grafana (the source of truth for
// role assignments, evaluated from SSO org_mapping at each login) who the
// caller is, what orgs they have, and with what role — then joins that
// with local Organization metadata so handlers receive a fully-populated
// Organization (tenants, datasources, role) in one shot. Deferring to
// Grafana avoids re-implementing its group-matching / precedence /
// casing rules on our side.
//
// All returned Organizations are deep-cloned; handler mutations cannot
// escape into any internal cache. An empty Caller (no Email, no Subject)
// returns ErrNoCallerIdentity.
type Authorizer interface {
	// RequireOrg returns the caller's Organization access to orgRef (matched
	// case-insensitively against Organization.Name or .DisplayName), or an
	// error classifying why access was denied: ErrNoCallerIdentity,
	// ErrOrgNotFound, ErrNotAuthorised, or ErrInsufficientRole.
	RequireOrg(ctx context.Context, caller Caller, orgRef string, minRole Role) (Organization, error)

	// ListOrgs returns every org the caller has a non-None role on, keyed
	// by Organization.Name. Empty map + nil error means "authenticated but
	// no accessible orgs".
	ListOrgs(ctx context.Context, caller Caller) (map[string]Organization, error)
}

// authorizer answers "what can this caller do?" by asking Grafana for the
// caller's org memberships and joining them against the OrgRegistry.
//
// The cache is keyed on OIDC subject (stable, non-spoofable). Email can
// change, be unverified, or be re-owned; subject cannot. Email is still
// used as the Grafana lookup input — Grafana stores users by email.
//
// Concurrent callers on a cold key share one upstream round-trip via
// singleflight; the LRU bounds long-running-process memory to
// CacheSize entries. Positive and negative entries carry different TTLs.
type authorizer struct {
	registry OrgRegistry
	grafana  OrgMembershipLookup
	log      *slog.Logger

	cache            *lru.Cache[string, cacheEntry]
	sf               singleflight.Group
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
}

// NewAuthorizer constructs an Authorizer with the given cache settings. Passing
// zero for any of the three cache parameters uses the DefaultCache*
// constants. cacheSize of -1 disables caching entirely (useful for tests).
func NewAuthorizer(registry OrgRegistry, grafana OrgMembershipLookup, log *slog.Logger, cacheTTL, negativeCacheTTL time.Duration, cacheSize int) (Authorizer, error) {
	if cacheTTL == 0 {
		cacheTTL = DefaultCacheTTL
	}
	if negativeCacheTTL == 0 {
		negativeCacheTTL = DefaultNegativeCacheTTL
	}
	if cacheSize == 0 {
		cacheSize = DefaultCacheSize
	}
	r := &authorizer{
		registry:         registry,
		grafana:          grafana,
		log:              log,
		cacheTTL:         cacheTTL,
		negativeCacheTTL: negativeCacheTTL,
	}
	if cacheSize > 0 {
		c, err := lru.New[string, cacheEntry](cacheSize)
		if err != nil {
			return nil, fmt.Errorf("authorizer cache: %w", err)
		}
		r.cache = c
	}
	return r, nil
}

// ListOrgs returns the caller's authorised orgs + role by asking Grafana and
// enriching with registry metadata. The returned map is deep-cloned so
// handler mutations cannot escape into the cache.
func (r *authorizer) ListOrgs(ctx context.Context, caller Caller) (map[string]Organization, error) {
	entry, err := r.resolveWithOrgs(ctx, caller)
	if err != nil {
		return nil, err
	}
	return cloneOrganizations(entry.orgs), nil
}

// RequireOrg returns the caller's access to orgRef, erroring if the org
// doesn't exist (ErrOrgNotFound), the caller isn't authorised for it
// (ErrNotAuthorised), or their role is below minRole.
//
// orgRef may be either the registry name or the displayName
// (case-insensitive). The returned Organization is deep-cloned so handler
// mutations cannot escape into the cache.
func (r *authorizer) RequireOrg(ctx context.Context, caller Caller, orgRef string, minRole Role) (Organization, error) {
	entry, err := r.resolveWithOrgs(ctx, caller)
	if err != nil {
		return Organization{}, err
	}

	if org, ok := findOrganization(entry.orgs, orgRef); ok {
		if !org.Role.AtLeast(minRole) {
			return Organization{}, ErrInsufficientRole(orgRef, org.Role, minRole)
		}
		return cloneOrganization(org), nil
	}

	// The caller doesn't have access. Disambiguate "org doesn't exist" vs
	// "caller not a member" using the registry set we already cached —
	// no extra List call needed.
	if _, knownOrg := entry.allOrgRefs[strings.ToLower(orgRef)]; !knownOrg {
		return Organization{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return Organization{}, ErrNotAuthorised(orgRef)
}

// resolveWithOrgs is the internal variant that returns the full cacheEntry
// (both the caller's access and the registry-ref set for Require's
// error-disambiguation). Callers outside this package should use ListOrgs.
func (r *authorizer) resolveWithOrgs(ctx context.Context, caller Caller) (cacheEntry, error) {
	if caller.Empty() {
		return cacheEntry{}, ErrNoCallerIdentity
	}

	key := cacheKey(caller)
	if hit, ok := r.cacheLookup(key); ok {
		return hit, nil
	}

	// Single-flight the cold path so concurrent callers on the same key
	// share one upstream round-trip instead of stampeding Grafana.
	v, err, _ := r.sf.Do(key, func() (any, error) {
		return r.load(ctx, caller, key)
	})
	if err != nil {
		return cacheEntry{}, err
	}
	return v.(cacheEntry), nil
}

// load does the actual upstream work for one cache key: Grafana lookup,
// Grafana user-orgs, registry list, and registry-to-access join (filling
// in Role from the Grafana membership). Caches the result with the
// appropriate positive-or-negative TTL.
func (r *authorizer) load(ctx context.Context, caller Caller, key string) (cacheEntry, error) {
	userID, found, err := r.grafana.LookupUserID(ctx, caller.Identity())
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana lookup: %w", err)
	}

	orgs, err := r.registry.List(ctx)
	if err != nil {
		return cacheEntry{}, fmt.Errorf("list orgs: %w", err)
	}
	allOrgRefs := buildOrgRefSet(orgs)

	if !found {
		// User exists in our IdP but has never logged into Grafana yet — we
		// genuinely don't know what orgs they have. Return empty + cache
		// briefly so the MCP tells them "log into Grafana first" without
		// locking them out for the full positive window.
		entry := cacheEntry{
			expiresAt:  time.Now().Add(r.negativeCacheTTL),
			orgs:       map[string]Organization{},
			allOrgRefs: allOrgRefs,
		}
		r.cacheStore(key, entry)
		return entry, nil
	}

	memberships, err := r.grafana.UserOrgs(ctx, userID)
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana user orgs: %w", err)
	}

	byOrgID := make(map[int64]Organization, len(orgs))
	for _, o := range orgs {
		byOrgID[o.OrgID] = o
	}

	out := make(map[string]Organization, len(memberships))
	for _, m := range memberships {
		role := roleFromGrafana(m.Role)
		if role == RoleNone {
			continue
		}
		org, ok := byOrgID[m.OrgID]
		if !ok {
			// Grafana knows about this org but the registry doesn't —
			// skip. Safe to ignore rather than leak non-registered orgs.
			continue
		}
		org.Role = role
		out[org.Name] = org
	}

	ttl := r.cacheTTL
	if len(out) == 0 {
		// Empty result is a negative — user exists in Grafana but has no
		// orgs we can show. Cache briefly.
		ttl = r.negativeCacheTTL
	}
	entry := cacheEntry{
		expiresAt:  time.Now().Add(ttl),
		orgs:       out,
		allOrgRefs: allOrgRefs,
	}
	r.cacheStore(key, entry)
	return entry, nil
}

// findOrganization locates an Organization by Name (exact) or by DisplayName
// (case-insensitive). Returns the found entry and true, or zero and false.
// The returned value aliases cache-owned slices — callers that hand the
// result to external code must clone via cloneOrganization.
func findOrganization(access map[string]Organization, orgRef string) (Organization, bool) {
	if org, ok := access[orgRef]; ok {
		return org, true
	}
	target := strings.ToLower(orgRef)
	for _, org := range access {
		if strings.ToLower(org.DisplayName) == target {
			return org, true
		}
	}
	return Organization{}, false
}
