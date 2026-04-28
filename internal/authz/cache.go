package authz

import (
	"slices"
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
)

// cacheEntry is one authorizer-cache entry. Holds only Grafana-side data
// (per-org Role memberships) — the registry join is recomputed from a
// fresh OrgLister.List on every RequireOrg / ListOrgs call so a deleted
// org disappears from a caller's accessible set immediately instead of
// hanging around for the rest of the cached caller's TTL.
//
// memberships nil → user not yet provisioned in Grafana (negative entry,
// short TTL); empty non-nil → user found, no org memberships (also
// negative). expiresAt is per-entry so positive and negative entries can
// age out at different rates.
type cacheEntry struct {
	expiresAt   time.Time
	memberships map[int64]Role
}

// cacheLookup returns the cached entry for key if one exists and is still
// fresh.
func (a *authorizer) cacheLookup(key string) (cacheEntry, bool) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	e, ok := a.cache[key]
	if !ok || !time.Now().Before(e.expiresAt) {
		return cacheEntry{}, false
	}
	return e, true
}

// cacheStore writes an entry under key, picking the positive or negative
// TTL based on memberships emptiness. ttl<=0 disables caching for that
// entry (used by tests that count upstream calls).
func (a *authorizer) cacheStore(key string, memberships map[int64]Role) cacheEntry {
	ttl := a.cacheTTL
	if len(memberships) == 0 {
		ttl = a.negativeCacheTTL
	}
	entry := cacheEntry{
		expiresAt:   time.Now().Add(ttl),
		memberships: memberships,
	}
	if ttl <= 0 {
		return entry
	}
	a.cacheMu.Lock()
	a.cache[key] = entry
	a.cacheMu.Unlock()
	return entry
}

// cacheKey returns the key under which Caller's access is cached. OIDC
// subject is the stable, non-spoofable identifier — and the only thing
// the cache trusts. resolveMemberships rejects empty-subject callers
// (Caller.Empty()) before this is reached, so the key is always non-empty.
func cacheKey(c Caller) string {
	return "sub:" + c.Subject
}

// buildOrgRefSet returns the set of lowercased Name + DisplayName values,
// used by RequireOrg to disambiguate "org not found" vs "not authorised"
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
// callers. Two concurrent callers projecting the same OrgLister.List
// slice must not be able to corrupt each other's view, so Tenants and
// Datasources are deep-copied. Strings and value-typed fields are copied
// by the struct-copy idiom at the call site.
func cloneOrganization(o Organization) Organization {
	o.Tenants = cloneTenants(o.Tenants)
	o.Datasources = slices.Clone(o.Datasources)
	return o
}

// cloneOrganizations deep-clones every Organization value in the map.
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
