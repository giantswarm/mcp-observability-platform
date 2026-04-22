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
	DefaultCacheSize        = 10000
)

// cacheEntry is one resolver-cache entry. It holds everything Require
// needs so the authorised path never re-lists the registry: access gives
// the caller's accessible orgs, and allOrgRefs is the name/displayname set
// used to pick the right error when access misses.
//
// expiresAt is per-entry rather than global so positive and negative hits
// can age out at different rates.
type cacheEntry struct {
	expiresAt  time.Time
	orgs       map[string]Organization
	allOrgRefs map[string]struct{} // lowercased Name + DisplayName
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
// subject is the stable, non-spoofable identifier and is always preferred.
// When no subject is present (unauthenticated test paths, legacy callers)
// we fall back to lowercased email so the resolver still functions; these
// paths shouldn't reach production because PromoteOAuthCaller populates
// Subject for authenticated callers.
func cacheKey(c Caller) string {
	if c.Subject != "" {
		return "sub:" + c.Subject
	}
	return "email:" + strings.ToLower(c.Email)
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
// corrupt the cache. Strings and value-typed fields are copied by the
// struct-copy idiom at the call site; only the slices need attention.
func cloneOrganization(o Organization) Organization {
	o.Tenants = cloneTenants(o.Tenants)
	o.Datasources = cloneDatasources(o.Datasources)
	return o
}

// cloneOrganizations deep-clones every Organization value in the map. Used
// only by Resolve which returns the full map to external callers.
// maps.Clone isn't a fit because it's shallow — we need cloneOrganization
// on each value.
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
