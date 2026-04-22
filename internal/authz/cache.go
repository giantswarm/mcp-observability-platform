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

// cachedAccess is one resolver-cache entry. It holds everything Require
// needs so the authorised path never re-lists CRs: access gives the caller's
// accessible orgs, and allOrgRefs is the name/displayname set used to pick
// the right error when access misses.
//
// expiresAt is per-entry rather than global so positive and negative hits
// can age out at different rates.
type cachedAccess struct {
	expiresAt  time.Time
	access     map[string]OrgAccess
	allOrgRefs map[string]struct{} // lowercased CR Name + DisplayName
}

// cacheLookup returns the cached entry for key if one exists and is still
// fresh. The returned entry aliases cache-owned slices; callers that hand
// OrgAccess values to external code must clone via cloneOrgAccess or
// cloneAccessMap.
func (r *Resolver) cacheLookup(key string) (cachedAccess, bool) {
	if r.cache == nil {
		return cachedAccess{}, false
	}
	hit, ok := r.cache.Get(key)
	if !ok || !time.Now().Before(hit.expiresAt) {
		return cachedAccess{}, false
	}
	return hit, true
}

// cacheStore writes an entry to the LRU. No-op when caching is disabled
// (cacheSize < 0 at construction time).
func (r *Resolver) cacheStore(key string, entry cachedAccess) {
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
func buildOrgRefSet(descs []OrgDescriptor) map[string]struct{} {
	out := make(map[string]struct{}, len(descs)*2)
	for _, d := range descs {
		out[strings.ToLower(d.Name)] = struct{}{}
		if d.DisplayName != "" {
			out[strings.ToLower(d.DisplayName)] = struct{}{}
		}
	}
	return out
}

// cloneOrgAccess returns a deep copy suitable for handing to external
// callers. Tenants and Datasources are deep-copied so a handler that
// appends to `oa.Tenants[i].Types` (or swaps a Datasource entry) cannot
// corrupt the cache. Strings and value-typed fields are copied by the
// struct-copy idiom at the call site; only the slices need attention.
func cloneOrgAccess(oa OrgAccess) OrgAccess {
	oa.Tenants = cloneTenants(oa.Tenants)
	oa.Datasources = cloneDatasources(oa.Datasources)
	return oa
}

// cloneAccessMap deep-clones every OrgAccess value in the map. Used only
// by Resolve which returns the full map to external callers. maps.Clone
// (Go 1.21+) isn't a fit because it's shallow — we need cloneOrgAccess on
// each value.
func cloneAccessMap(in map[string]OrgAccess) map[string]OrgAccess {
	if in == nil {
		return nil
	}
	out := make(map[string]OrgAccess, len(in))
	for k, v := range in {
		out[k] = cloneOrgAccess(v)
	}
	return out
}
