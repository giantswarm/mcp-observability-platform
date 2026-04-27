package authz

import (
	"strings"
	"time"
)

// Cache defaults. Positive entries are cached `DefaultCacheTTL`; negative
// entries (user-not-found, empty-memberships) use `DefaultNegativeCacheTTL`.
// Negative TTL is deliberately short so a mid-SSO-outage failed lookup
// doesn't lock a user out for the full positive window.
const (
	DefaultCacheTTL         = 30 * time.Second
	DefaultNegativeCacheTTL = 5 * time.Second
	// DefaultCacheSize bounds memory on long-running pods with many distinct
	// OIDC subjects (large tenants + SSO-forwarded callers). At ~5 KiB per
	// entry (two maps + a dozen orgs worst-case), 10k subjects ≈ 50 MiB — an
	// order of magnitude below pod RSS while still big enough that realistic
	// churn doesn't evict hot callers.
	DefaultCacheSize = 10000
)

// cacheEntry is one authorizer-cache entry. It holds only the
// Grafana-side data (user ID + per-org Role memberships) — the registry
// join is recomputed from a fresh OrgRegistry.List on every RequireOrg /
// ListOrgs call. The registry is in-memory (controller-runtime informer
// cache) so the join is trivially cheap, and recomputing it means a
// deleted org disappears from a caller's accessible set immediately
// instead of hanging around for the rest of the cached caller's TTL.
//
// memberships nil means "user not yet provisioned in Grafana" (negative
// entry, short TTL); empty non-nil means "user found, no org
// memberships" (also negative). expiresAt is per-entry so positive and
// negative entries can age out at different rates.
type cacheEntry struct {
	expiresAt   time.Time
	memberships map[int64]Role
}

// cacheLookup returns the cached entry for key if one exists and is still
// fresh. The returned entry aliases cache-owned slices; callers that hand
// Organization values to external code must clone via cloneOrganization
// or cloneOrganizations.
func (r *authorizer) cacheLookup(key string) (cacheEntry, bool) {
	if r.cache == nil {
		return cacheEntry{}, false
	}
	hit, ok := r.cache.Get(key)
	if !ok || !time.Now().Before(hit.expiresAt) {
		return cacheEntry{}, false
	}
	return hit, true
}

// cacheStore writes an entry to the LRU. No-op when caching is disabled
// (cacheSize < 0 at construction time).
func (r *authorizer) cacheStore(key string, entry cacheEntry) {
	if r.cache == nil {
		return
	}
	r.cache.Add(key, entry)
}

// cacheKey returns the key under which Caller's access is cached. OIDC
// subject is the stable, non-spoofable identifier — and the only thing
// the cache trusts. resolveWithOrgs guards against empty-subject callers
// before this is reached (Caller.Empty() rejects them), so the key is
// always non-empty here.
func cacheKey(c Caller) string {
	return "sub:" + c.Subject
}

// buildOrgRefSet returns the set of lowercased Name + DisplayName values,
// used by Require to disambiguate "org not found" vs "not authorised"
// without a second upstream call.
func buildOrgRefSet(orgs []Organization) map[string]struct{} {
	out := make(map[string]struct{}, len(orgs)*2)
	for _, o := range orgs {
		out[strings.ToLower(o.Name)] = struct{}{}
		if o.DisplayName != "" {
			out[strings.ToLower(o.DisplayName)] = struct{}{}
		}
	}
	return out
}

// cloneOrganization returns a deep copy suitable for handing to external
// callers. Tenants and Datasources are deep-copied so a handler that
// appends to `org.Tenants[i].Types` (or swaps a Datasource entry) cannot
// corrupt anything shared. Strings and value-typed fields are copied by
// the struct-copy idiom at the call site; only the slices need
// attention.
//
// Today the registry-side Organization values are read fresh per call
// (the cache only holds Grafana-side memberships), so handler mutations
// can no longer escape into the cache by definition. We still
// deep-clone here so a single per-call OrgRegistry.List that gets
// projected for two concurrent callers cannot be corrupted by either.
func cloneOrganization(o Organization) Organization {
	o.Tenants = cloneTenants(o.Tenants)
	o.Datasources = cloneDatasources(o.Datasources)
	return o
}

// cloneOrganizations deep-clones every Organization value in the map.
// Used by ListOrgs which returns the full per-caller projection.
// maps.Clone isn't a fit — it's shallow, and an Organization carries
// Tenants/Datasources slices.
func cloneOrganizations(in map[string]Organization) map[string]Organization {
	if in == nil {
		return nil
	}
	out := make(map[string]Organization, len(in))
	for k, v := range in {
		out[k] = cloneOrganization(v)
	}
	return out
}
