// Package authz resolves a caller's identity to the set of Grafana
// organisations and role they may access.
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
//   - resolver.go — Resolver + Resolve/Require/load (this file).
//   - cache.go    — LRU + singleflight + TTL + clone discipline.
//   - role.go     — Role enum.
//   - access.go   — OrgAccess value type + helpers.
//   - caller.go   — Caller + OrgRegistry + OrgMembershipLookup ports.
//   - types.go    — Tenant / Datasource / TenantType / OrgDescriptor
//     domain types (tool-handler consumers import these, never the CRD).
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

// Resolver answers "what can this caller do?" by asking Grafana for the
// caller's org memberships and joining them against the OrgRegistry.
//
// The cache is keyed on OIDC subject (stable, non-spoofable). Email can
// change, be unverified, or be re-owned; subject cannot. Email is still
// used as the Grafana lookup input — Grafana stores users by email.
//
// Concurrent callers on a cold key share one upstream round-trip via
// singleflight; the LRU bounds long-running-process memory to
// CacheSize entries. Positive and negative entries carry different TTLs.
type Resolver struct {
	registry OrgRegistry
	grafana  OrgMembershipLookup
	log      *slog.Logger

	cache            *lru.Cache[string, cachedAccess]
	sf               singleflight.Group
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
}

// NewResolver constructs a Resolver with the given cache settings. Passing
// zero for any of the three cache parameters uses the DefaultCache*
// constants. cacheSize of -1 disables caching entirely (useful for tests).
func NewResolver(registry OrgRegistry, grafana OrgMembershipLookup, log *slog.Logger, cacheTTL, negativeCacheTTL time.Duration, cacheSize int) (*Resolver, error) {
	if cacheTTL == 0 {
		cacheTTL = DefaultCacheTTL
	}
	if negativeCacheTTL == 0 {
		negativeCacheTTL = DefaultNegativeCacheTTL
	}
	if cacheSize == 0 {
		cacheSize = DefaultCacheSize
	}
	r := &Resolver{
		registry:         registry,
		grafana:          grafana,
		log:              log,
		cacheTTL:         cacheTTL,
		negativeCacheTTL: negativeCacheTTL,
	}
	if cacheSize > 0 {
		c, err := lru.New[string, cachedAccess](cacheSize)
		if err != nil {
			return nil, fmt.Errorf("resolver cache: %w", err)
		}
		r.cache = c
	}
	return r, nil
}

// Resolve returns the caller's authorised orgs + role by asking Grafana and
// enriching with registry metadata. The returned map is deep-cloned so
// handler mutations cannot escape into the cache.
func (r *Resolver) Resolve(ctx context.Context, caller Caller) (map[string]OrgAccess, error) {
	entry, err := r.resolveWithOrgs(ctx, caller)
	if err != nil {
		return nil, err
	}
	return cloneAccessMap(entry.access), nil
}

// Require returns the caller's access to orgRef, erroring if the org
// doesn't exist (ErrOrgNotFound), the caller isn't authorised for it
// (ErrNotAuthorised), or their role is below minRole.
//
// orgRef may be either the registry name or the displayName
// (case-insensitive). The returned OrgAccess is deep-cloned so handler
// mutations cannot escape into the cache.
func (r *Resolver) Require(ctx context.Context, caller Caller, orgRef string, minRole Role) (OrgAccess, error) {
	entry, err := r.resolveWithOrgs(ctx, caller)
	if err != nil {
		return OrgAccess{}, err
	}

	if oa, ok := findAccess(entry.access, orgRef); ok {
		if !oa.Role.AtLeast(minRole) {
			return OrgAccess{}, ErrInsufficientRole(orgRef, oa.Role, minRole)
		}
		return cloneOrgAccess(oa), nil
	}

	// The caller doesn't have access. Disambiguate "org doesn't exist" vs
	// "caller not a member" using the registry set we already cached —
	// no extra List call needed.
	if _, knownOrg := entry.allOrgRefs[strings.ToLower(orgRef)]; !knownOrg {
		return OrgAccess{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return OrgAccess{}, ErrNotAuthorised(orgRef)
}

// resolveWithOrgs is the internal variant that returns the full cachedAccess
// (both the caller's access and the registry-ref set for Require's
// error-disambiguation). Callers outside this package should use Resolve.
func (r *Resolver) resolveWithOrgs(ctx context.Context, caller Caller) (cachedAccess, error) {
	if caller.Empty() {
		return cachedAccess{}, ErrNoCallerIdentity
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
		return cachedAccess{}, err
	}
	return v.(cachedAccess), nil
}

// load does the actual upstream work for one cache key: Grafana lookup,
// Grafana user-orgs, registry list, and descriptor-to-access join. Caches
// the result with the appropriate positive-or-negative TTL.
func (r *Resolver) load(ctx context.Context, caller Caller, key string) (cachedAccess, error) {
	userID, found, err := r.grafana.LookupUserID(ctx, caller.Identity())
	if err != nil {
		return cachedAccess{}, fmt.Errorf("grafana lookup: %w", err)
	}

	descs, err := r.registry.List(ctx)
	if err != nil {
		return cachedAccess{}, fmt.Errorf("list orgs: %w", err)
	}
	allOrgRefs := buildOrgRefSet(descs)

	if !found {
		// User exists in our IdP but has never logged into Grafana yet — we
		// genuinely don't know what orgs they have. Return empty + cache
		// briefly so the MCP tells them "log into Grafana first" without
		// locking them out for the full positive window.
		entry := cachedAccess{
			expiresAt:  time.Now().Add(r.negativeCacheTTL),
			access:     map[string]OrgAccess{},
			allOrgRefs: allOrgRefs,
		}
		r.cacheStore(key, entry)
		return entry, nil
	}

	memberships, err := r.grafana.UserOrgs(ctx, userID)
	if err != nil {
		return cachedAccess{}, fmt.Errorf("grafana user orgs: %w", err)
	}

	byOrgID := make(map[int64]OrgDescriptor, len(descs))
	for _, d := range descs {
		byOrgID[d.OrgID] = d
	}

	out := make(map[string]OrgAccess, len(memberships))
	for _, m := range memberships {
		role := roleFromGrafana(m.Role)
		if role == RoleNone {
			continue
		}
		desc, ok := byOrgID[m.OrgID]
		if !ok {
			// Grafana knows about this org but the registry doesn't —
			// skip. Safe to ignore rather than leak non-registered orgs.
			continue
		}
		out[desc.Name] = descriptorToAccess(desc, role)
	}

	ttl := r.cacheTTL
	if len(out) == 0 {
		// Empty result is a negative — user exists in Grafana but has no
		// orgs we can show. Cache briefly.
		ttl = r.negativeCacheTTL
	}
	entry := cachedAccess{
		expiresAt:  time.Now().Add(ttl),
		access:     out,
		allOrgRefs: allOrgRefs,
	}
	r.cacheStore(key, entry)
	return entry, nil
}

// findAccess locates an OrgAccess by CR name (exact) or by DisplayName
// (case-insensitive). Returns the found entry and true, or zero and false.
// The returned value aliases cache-owned slices — callers that hand the
// result to external code must clone via cloneOrgAccess.
func findAccess(access map[string]OrgAccess, orgRef string) (OrgAccess, bool) {
	if oa, ok := access[orgRef]; ok {
		return oa, true
	}
	target := strings.ToLower(orgRef)
	for _, oa := range access {
		if strings.ToLower(oa.DisplayName) == target {
			return oa, true
		}
	}
	return OrgAccess{}, false
}
