package authz

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
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
// escape into any internal cache. The caller is derived from ctx via
// CallerFromContext; an empty Caller (no Email, no Subject) returns
// ErrNoCallerIdentity. The framework-level RequireCaller middleware
// blocks calls without a caller before any handler runs, so this guard
// is belt-and-braces.
type Authorizer interface {
	// RequireOrg returns the caller's Organization access to orgRef (matched
	// case-insensitively against Organization.Name or .DisplayName), or an
	// error classifying why access was denied: ErrNoCallerIdentity,
	// ErrOrgNotFound, ErrNotAuthorised, or ErrInsufficientRole.
	RequireOrg(ctx context.Context, orgRef string, minRole Role) (Organization, error)

	// ListOrgs returns every org the caller has a non-None role on, keyed
	// by Organization.Name. Empty map + nil error means "authenticated but
	// no accessible orgs".
	ListOrgs(ctx context.Context) (map[string]Organization, error)
}

// authorizer answers "what can this caller do?" by asking Grafana for
// the caller's org memberships and joining them against the org list
// returned by OrgLister.
//
// The cache is keyed on OIDC subject (stable, non-spoofable) and uses
// a single TTL. A freshly-provisioned user waits one TTL window before
// access works.
type authorizer struct {
	orgs    OrgLister
	grafana grafana.Client
	log     *slog.Logger
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewAuthorizer constructs an Authorizer. ttl=0 uses DefaultCacheTTL.
// Pass a negative ttl to disable caching (useful for tests).
func NewAuthorizer(orgs OrgLister, grafana grafana.Client, log *slog.Logger, ttl time.Duration) (Authorizer, error) {
	if ttl == 0 {
		ttl = DefaultCacheTTL
	}
	return &authorizer{
		orgs:    orgs,
		grafana: grafana,
		log:     log,
		ttl:     ttl,
		cache:   map[string]cacheEntry{},
	}, nil
}

// ListOrgs returns the caller's authorised orgs + role by asking Grafana
// and enriching each with the current org metadata. The returned map is
// deep-cloned so handler mutations cannot leak. Caller identity is read
// from ctx via CallerFromContext.
//
// The org list is read fresh every call — only the per-caller Grafana
// memberships are cached. A GrafanaOrganization deletion takes effect
// immediately for every caller, not after their TTL expires.
func (r *authorizer) ListOrgs(ctx context.Context) (map[string]Organization, error) {
	memberships, err := r.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	orgs, err := r.orgs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	out, _ := projectMemberships(memberships, orgs)
	return cloneOrganizations(out), nil
}

// RequireOrg returns the caller's access to orgRef, erroring if the org
// doesn't exist (ErrOrgNotFound), the caller isn't authorised for it
// (ErrNotAuthorised), their role is below minRole (ErrInsufficientRole),
// or orgRef matches more than one Organization by DisplayName
// (ErrAmbiguousOrgRef).
//
// orgRef may be either the org's Name or DisplayName
// (case-insensitive). minRole MUST be RoleViewer or higher: passing
// RoleNone returns ErrInvalidMinRole rather than silently passing every
// authorised caller through (Role.AtLeast(RoleNone) is always true).
//
// Caller identity is read from ctx via CallerFromContext. The returned
// Organization is deep-cloned so handler mutations cannot escape. The
// org list is read fresh every call — a GrafanaOrganization deletion
// is invisible to subsequent RequireOrg calls.
func (r *authorizer) RequireOrg(ctx context.Context, orgRef string, minRole Role) (Organization, error) {
	if minRole == RoleNone {
		return Organization{}, ErrInvalidMinRole
	}
	memberships, err := r.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return Organization{}, err
	}
	orgs, err := r.orgs.List(ctx)
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
			return Organization{}, ErrInsufficientRole(orgRef, org.Role, minRole)
		}
		return cloneOrganization(org), nil
	}

	// Disambiguate "org doesn't exist" vs "caller not a member of an
	// existing org" against the freshly-listed orgs, not a cached
	// snapshot.
	if _, knownOrg := allRefs[strings.ToLower(orgRef)]; !knownOrg {
		return Organization{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return Organization{}, ErrNotAuthorised(orgRef)
}

// resolveMemberships returns the caller's per-org Role assignments
// (OrgID → Role), as observed by Grafana. Cache hit returns the
// snapshot; miss falls back to the load() upstream call.
//
// Concurrent callers on a cold key each fan out a Grafana call —
// acceptable at our scale (~50 users, deploy-time stampede is a
// handful of duplicate calls per pod, not thousands).
func (r *authorizer) resolveMemberships(ctx context.Context, caller Caller) (map[int64]Role, error) {
	if !caller.Authenticated() {
		return nil, ErrNoCallerIdentity
	}
	key := cacheKey(caller)
	if hit, ok := r.cacheLookup(key); ok {
		return hit.memberships, nil
	}
	entry, err := r.load(ctx, caller, key)
	if err != nil {
		return nil, err
	}
	return entry.memberships, nil
}

// projectMemberships joins per-caller Grafana memberships against the
// current org list. Returns the caller's accessible orgs (keyed by
// Organization.Name, with Role filled in) plus the lowercased set of
// every {Name, DisplayName} in the org list — used by RequireOrg to
// distinguish "org doesn't exist" from "caller not a member".
func projectMemberships(memberships map[int64]Role, orgs []Organization) (access map[string]Organization, allRefs map[string]struct{}) {
	access = make(map[string]Organization, len(memberships))
	allRefs = make(map[string]struct{}, len(orgs)*2)
	byOrgID := make(map[int64]Organization, len(orgs))
	for _, o := range orgs {
		byOrgID[o.OrgID] = o
		allRefs[strings.ToLower(o.Name)] = struct{}{}
		if o.DisplayName != "" {
			allRefs[strings.ToLower(o.DisplayName)] = struct{}{}
		}
	}
	for orgID, role := range memberships {
		if role == RoleNone {
			continue
		}
		// Grafana sometimes lists orgs the registry doesn't carry (deleted
		// or never provisioned). Skip silently — callers must never see
		// non-registered orgs.
		org, ok := byOrgID[orgID]
		if !ok {
			continue
		}
		org.Role = role
		access[org.Name] = org
	}
	return access, allRefs
}

// load does the per-caller upstream work: ask Grafana for the user and
// their org memberships, then cache OrgID → Role. The join with the
// org list happens at the per-call site (projectMemberships) — load
// doesn't see Organization values.
func (r *authorizer) load(ctx context.Context, caller Caller, key string) (cacheEntry, error) {
	user, err := r.grafana.LookupUser(ctx, caller.Identity())
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana lookup: %w", err)
	}

	var memberships map[int64]Role
	if user != nil {
		rawMemberships, err := r.grafana.UserOrgs(ctx, user.ID)
		if err != nil {
			return cacheEntry{}, fmt.Errorf("grafana user orgs: %w", err)
		}
		memberships = make(map[int64]Role, len(rawMemberships))
		for _, m := range rawMemberships {
			role := roleFromGrafana(m.Role)
			if role == RoleNone {
				continue
			}
			memberships[m.OrgID] = role
		}
	}
	// memberships nil => user not provisioned yet in Grafana (cached so
	// the next call doesn't re-hit /api/users/lookup for one TTL window).
	entry := cacheEntry{
		expiresAt:   time.Now().Add(r.ttl),
		memberships: memberships,
	}
	r.cacheStore(key, entry)
	return entry, nil
}

// findOrganization locates an Organization by Name (exact) or by DisplayName
// (case-insensitive). Exact-Name match wins over DisplayName matches even
// when the DisplayName lookup would also match (so an org Name="prod" with
// DisplayName="staging" coexists fine with an org Name="staging").
//
// Returns ErrAmbiguousOrgRef when more than one DisplayName matches —
// silently picking the first map-iteration hit would be unsafe for an
// authz boundary. The returned Organization aliases cache-owned slices;
// the caller (RequireOrg) clones before returning to handlers.
func findOrganization(access map[string]Organization, orgRef string) (Organization, bool, error) {
	if org, ok := access[orgRef]; ok {
		return org, true, nil
	}
	target := strings.ToLower(orgRef)
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
