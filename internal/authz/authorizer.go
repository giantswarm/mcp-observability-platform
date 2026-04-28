package authz

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// Authorizer decides whether a caller may act on a given Grafana org, and at
// what Role. Tool handlers hold one and call RequireOrg() before touching
// any datasource.
//
// The production implementation asks Grafana (the source of truth for role
// assignments, evaluated from SSO org_mapping at each login) who the caller
// is, what orgs they have, and with what role — then joins that with local
// Organization metadata so handlers receive a fully-populated Organization
// (tenants, datasources, role) in one shot. Returned Organizations are
// deep-cloned; handler mutations cannot escape into any internal cache.
//
// Caller is derived from ctx via CallerFromContext; an empty Caller (no
// Subject) returns ErrNoCallerIdentity.
type Authorizer interface {
	// RequireOrg returns the caller's Organization access to orgRef
	// (matched case-insensitively against Organization.Name or
	// .DisplayName), or an error wrapping one of: ErrNoCallerIdentity,
	// ErrOrgNotFound, ErrNotAuthorised, ErrInsufficientRole,
	// ErrAmbiguousOrgRef, or ErrInvalidMinRole.
	RequireOrg(ctx context.Context, orgRef string, minRole Role) (Organization, error)

	// ListOrgs returns every org the caller has a non-None role on, keyed
	// by Organization.Name. Empty map + nil error means "authenticated but
	// no accessible orgs".
	ListOrgs(ctx context.Context) (map[string]Organization, error)
}

type authorizer struct {
	orgs    OrgLister
	grafana grafana.Client

	cacheMu          sync.RWMutex
	cache            map[string]cacheEntry
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
}

// NewAuthorizer constructs an Authorizer. cacheTTL/negativeCacheTTL <= 0
// disables caching for that polarity (every call re-fetches from Grafana —
// useful for tests that count upstream calls).
func NewAuthorizer(orgs OrgLister, gc grafana.Client, cacheTTL, negativeCacheTTL time.Duration) Authorizer {
	return &authorizer{
		orgs:             orgs,
		grafana:          gc,
		cache:            make(map[string]cacheEntry),
		cacheTTL:         cacheTTL,
		negativeCacheTTL: negativeCacheTTL,
	}
}

// ListOrgs returns the caller's authorised orgs + role by asking Grafana
// and enriching with the current registry metadata. The returned map is
// deep-cloned so handler mutations cannot leak. Caller identity is read
// from ctx via CallerFromContext.
//
// The registry list is read fresh every call — only the per-caller
// Grafana memberships are cached. A GrafanaOrganization deletion takes
// effect immediately for every caller, not after their TTL expires.
func (a *authorizer) ListOrgs(ctx context.Context) (map[string]Organization, error) {
	memberships, err := a.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	orgs, err := a.orgs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	out, _ := projectMemberships(memberships, orgs)
	return cloneOrganizations(out), nil
}

// RequireOrg returns the caller's access to orgRef, erroring with
// ErrOrgNotFound, ErrNotAuthorised, ErrInsufficientRole, or
// ErrAmbiguousOrgRef as appropriate. orgRef matches Organization.Name or
// DisplayName case-insensitively; Name takes precedence.
//
// minRole MUST be RoleViewer or higher: passing RoleNone returns
// ErrInvalidMinRole rather than silently passing every authorised caller
// through (Role.AtLeast(RoleNone) is always true).
//
// Caller identity is read from ctx via CallerFromContext. The returned
// Organization is deep-cloned so handler mutations cannot escape.
func (a *authorizer) RequireOrg(ctx context.Context, orgRef string, minRole Role) (Organization, error) {
	if minRole == RoleNone {
		return Organization{}, ErrInvalidMinRole
	}
	memberships, err := a.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return Organization{}, err
	}
	orgs, err := a.orgs.List(ctx)
	if err != nil {
		return Organization{}, fmt.Errorf("list orgs: %w", err)
	}
	access, allRefs := projectMemberships(memberships, orgs)

	org, ok, err := findOrganization(access, orgRef)
	if err != nil {
		return Organization{}, err
	}
	if ok {
		if !org.Role.AtLeast(minRole) {
			return Organization{}, insufficientRoleFor(orgRef, org.Role, minRole)
		}
		return cloneOrganization(org), nil
	}

	if _, knownOrg := allRefs[strings.ToLower(orgRef)]; !knownOrg {
		return Organization{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return Organization{}, notAuthorisedFor(orgRef)
}

// resolveMemberships returns the caller's per-org Role assignments
// (OrgID → Role) as observed by Grafana. Hits the per-caller cache before
// falling back to load(). Caller-level validation (ErrNoCallerIdentity)
// happens here.
func (a *authorizer) resolveMemberships(ctx context.Context, caller Caller) (map[int64]Role, error) {
	if caller.Empty() {
		return nil, ErrNoCallerIdentity
	}
	key := cacheKey(caller)
	if hit, ok := a.cacheLookup(key); ok {
		return hit.memberships, nil
	}
	entry, err := a.load(ctx, caller, key)
	if err != nil {
		return nil, err
	}
	return entry.memberships, nil
}

// projectMemberships joins per-caller Grafana memberships against the
// current registry list. Returns the caller's accessible orgs (keyed by
// Organization.Name, with Role filled in) plus the lowercased set of
// every {Name, DisplayName} in the registry — used by RequireOrg to
// distinguish "org doesn't exist" from "caller not a member".
func projectMemberships(memberships map[int64]Role, orgs []Organization) (access map[string]Organization, allRefs map[string]struct{}) {
	allRefs = buildOrgRefSet(orgs)
	access = make(map[string]Organization, len(memberships))
	if len(memberships) == 0 {
		return access, allRefs
	}
	byOrgID := make(map[int64]Organization, len(orgs))
	for _, o := range orgs {
		byOrgID[o.OrgID] = o
	}
	for orgID, role := range memberships {
		// Defensive: load filters RoleNone, but memberships is map-valued
		// and a future code path could insert.
		if role == RoleNone {
			continue
		}
		org, ok := byOrgID[orgID]
		if !ok {
			// Grafana knows about this org but the registry doesn't —
			// org was deleted (or never registered). Skip silently.
			continue
		}
		org.Role = role
		access[org.Name] = org
	}
	return access, allRefs
}

// load asks Grafana for the user and their org memberships, then caches
// OrgID → Role with the appropriate positive-or-negative TTL. The
// registry-side join happens at the per-call site (projectMemberships).
func (a *authorizer) load(ctx context.Context, caller Caller, key string) (cacheEntry, error) {
	user, err := a.grafana.LookupUser(ctx, caller.Identity())
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana lookup: %w", err)
	}

	if user == nil {
		// User exists in our IdP but has never logged into Grafana yet —
		// negative-cache nil memberships so the caller hits a brief "log
		// into Grafana first" window, not the full positive window.
		// Distinct from len(memberships)==0 (user found, in no orgs) —
		// same TTL but different semantics.
		return a.cacheStore(key, nil), nil
	}

	rawMemberships, err := a.grafana.UserOrgs(ctx, user.ID)
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana user orgs: %w", err)
	}

	memberships := make(map[int64]Role, len(rawMemberships))
	for _, m := range rawMemberships {
		role := roleFromGrafana(m.Role)
		if role == RoleNone {
			continue
		}
		memberships[m.OrgID] = role
	}
	return a.cacheStore(key, memberships), nil
}

// findOrganization locates an Organization by Name or DisplayName,
// case-insensitively. Name match wins over DisplayName matches (so an org
// Name="prod" / DisplayName="staging" coexists fine with an org
// Name="staging").
//
// Returns ErrAmbiguousOrgRef when more than one DisplayName matches —
// silently picking the first map-iteration hit would be unsafe for an
// authz boundary. The returned value aliases cache-owned slices —
// callers handing the result to external code must clone via
// cloneOrganization.
func findOrganization(access map[string]Organization, orgRef string) (Organization, bool, error) {
	target := strings.ToLower(orgRef)
	for _, org := range access {
		if strings.ToLower(org.Name) == target {
			return org, true, nil
		}
	}
	var matches []Organization
	for _, org := range access {
		if strings.ToLower(org.DisplayName) == target {
			matches = append(matches, org)
		}
	}
	switch len(matches) {
	case 0:
		return Organization{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return Organization{}, false, fmt.Errorf("%w: %q matches %d organizations by displayName", ErrAmbiguousOrgRef, orgRef, len(matches))
	}
}
