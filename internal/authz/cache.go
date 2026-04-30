package authz

import (
	"time"
)

// Cache defaults. Positive entries are cached `DefaultCacheTTL`; negative
// entries (user-not-found, no roles) use `DefaultNegativeCacheTTL`.
// Negative TTL is deliberately short so a mid-SSO-outage failed lookup
// doesn't lock a user out for the full positive window.
const (
	DefaultCacheTTL         = 30 * time.Second
	DefaultNegativeCacheTTL = 5 * time.Second
)

// cacheStatus distinguishes the two negative shapes a cached entry can
// have. statusUnknownToGrafana means the IdP knows the user but Grafana
// doesn't — surfaced as ErrCallerUnknownToGrafana so list_orgs can tell
// them to log into Grafana once. statusKnown covers everything else,
// including "user has zero org roles".
type cacheStatus int

const (
	statusUnknownToGrafana cacheStatus = iota
	statusKnown
)

// cacheEntry is one authorizer-cache entry. Holds only Grafana-side data
// (per-org Role assignments) — the registry join is recomputed from a
// fresh OrgLister.List on every RequireOrg / ListOrgs call so a deleted
// org disappears from a caller's accessible set immediately instead of
// hanging around for the rest of the cached caller's TTL. expiresAt is
// per-entry so positive and negative entries can age out at different
// rates.
type cacheEntry struct {
	expiresAt time.Time
	status    cacheStatus
	roles     map[int64]Role
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
// TTL based on status + emptiness. ttl<=0 disables caching for that
// entry (used by tests that count upstream calls).
func (a *authorizer) cacheStore(key string, status cacheStatus, roles map[int64]Role) cacheEntry {
	ttl := a.cacheTTL
	if status == statusUnknownToGrafana || len(roles) == 0 {
		ttl = a.negativeCacheTTL
	}
	entry := cacheEntry{
		expiresAt: time.Now().Add(ttl),
		status:    status,
		roles:     roles,
	}
	if ttl <= 0 {
		return entry
	}
	a.cacheMu.Lock()
	a.cache[key] = entry
	a.cacheMu.Unlock()
	return entry
}

// cloneOrganization deep-copies the Tenants slice so handler-side
// mutations cannot leak back into the cached registry view.
func cloneOrganization(o Organization) Organization {
	o.Tenants = cloneTenants(o.Tenants)
	return o
}

// cloneOrganizations deep-clones every Organization in the map.
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
