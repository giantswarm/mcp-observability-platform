package authz

import (
	"slices"
	"time"
)

// DefaultCacheTTL is how long a per-caller membership snapshot stays
// fresh before the next access re-asks Grafana. 30s is the documented
// freshness guarantee for org membership / role changes; revoked roles
// keep working until their entry expires (accepted trade-off vs paying
// the Grafana lookup latency on every call).
//
// A plain mutex-guarded map is enough: ~50 users / ~10 orgs, distinct
// subjects bounded by user count, entries are a map[int64]Role each.
const DefaultCacheTTL = 30 * time.Second

// cacheEntry is one authorizer-cache entry. Holds only the Grafana-side
// membership snapshot — the registry-side join is recomputed from a
// fresh OrgLister.List on every RequireOrg / ListOrgs call.
//
// memberships nil means "user not yet provisioned in Grafana"; empty
// non-nil means "user found, no org memberships". Both are valid
// outcomes and cached identically.
type cacheEntry struct {
	expiresAt   time.Time
	memberships map[int64]Role
}

// cacheLookup returns the cached entry for key if one exists and is
// still fresh.
func (r *authorizer) cacheLookup(key string) (cacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hit, ok := r.cache[key]
	if !ok || !time.Now().Before(hit.expiresAt) {
		return cacheEntry{}, false
	}
	return hit, true
}

func (r *authorizer) cacheStore(key string, entry cacheEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = entry
}

// cacheKey returns the key under which Caller's access is cached. OIDC
// subject is the stable, non-spoofable identifier — and the only thing
// the cache trusts. resolveMemberships rejects unauthenticated callers
// before this is reached, so the key is always non-empty here.
func cacheKey(c Caller) string {
	return "sub:" + c.Subject
}

// cloneOrganization returns a deep copy. Tenants and Datasources are
// deep-copied so a handler that appends to org.Tenants[i].Types (or
// swaps a Datasource entry) cannot corrupt anything shared. Strings and
// value-typed fields are copied by the struct-copy idiom.
func cloneOrganization(o Organization) Organization {
	o.Tenants = cloneTenants(o.Tenants)
	o.Datasources = slices.Clone(o.Datasources)
	return o
}

// cloneOrganizations deep-clones every Organization value in the map.
// Used by ListOrgs which returns the full per-caller projection.
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
