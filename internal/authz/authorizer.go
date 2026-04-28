package authz

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

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

// authorizer answers "what can this caller do?" by asking Grafana for the
// caller's org memberships and joining them against the OrgLister.
//
// The cache is keyed on OIDC subject (stable, non-spoofable). Email can
// change, be unverified, or be re-owned; subject cannot. Email is still
// used as the Grafana lookup input — Grafana stores users by email.
//
// Concurrent callers on a cold key share one upstream round-trip via
// singleflight; the LRU bounds long-running-process memory to
// CacheSize entries. Positive and negative entries carry different TTLs.
type authorizer struct {
	registry OrgLister
	grafana  grafana.Client
	log      *slog.Logger

	cache            *lru.Cache[string, cacheEntry]
	sf               singleflight.Group
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
}

// NewAuthorizer constructs an Authorizer with the given cache settings. Passing
// zero for any of the three cache parameters uses the DefaultCache*
// constants. cacheSize of -1 disables caching entirely (useful for tests).
func NewAuthorizer(registry OrgLister, grafana grafana.Client, log *slog.Logger, cacheTTL, negativeCacheTTL time.Duration, cacheSize int) (Authorizer, error) {
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

// ListOrgs returns the caller's authorised orgs + role by asking Grafana
// and enriching with the current registry metadata. The returned map is
// deep-cloned so handler mutations cannot leak. Caller identity is read
// from ctx via CallerFromContext.
//
// The registry list is read fresh every call — only the per-caller
// Grafana memberships are cached. A GrafanaOrganization deletion takes
// effect immediately for every caller, not after their TTL expires.
func (r *authorizer) ListOrgs(ctx context.Context) (map[string]Organization, error) {
	memberships, err := r.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	orgs, err := r.registry.List(ctx)
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
// orgRef may be either the registry name or the displayName
// (case-insensitive). minRole MUST be RoleViewer or higher: passing
// RoleNone returns ErrInvalidMinRole rather than silently passing every
// authorised caller through (Role.AtLeast(RoleNone) is always true).
//
// Caller identity is read from ctx via CallerFromContext. The returned
// Organization is deep-cloned so handler mutations cannot escape. The
// registry list is read fresh every call — a GrafanaOrganization
// deletion is invisible to subsequent RequireOrg calls.
func (r *authorizer) RequireOrg(ctx context.Context, orgRef string, minRole Role) (Organization, error) {
	if minRole == RoleNone {
		return Organization{}, ErrInvalidMinRole
	}
	memberships, err := r.resolveMemberships(ctx, CallerFromContext(ctx))
	if err != nil {
		return Organization{}, err
	}
	orgs, err := r.registry.List(ctx)
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
	// existing org" against the freshly-listed registry, not a cached
	// snapshot.
	if _, knownOrg := allRefs[strings.ToLower(orgRef)]; !knownOrg {
		return Organization{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return Organization{}, ErrNotAuthorised(orgRef)
}

// resolveMemberships returns the caller's per-org Role assignments
// (OrgID → Role), as observed by Grafana. Hits the per-caller LRU
// before falling back to the load() upstream call. Caller-level
// validation (ErrNoCallerIdentity) happens here.
func (r *authorizer) resolveMemberships(ctx context.Context, caller Caller) (map[int64]Role, error) {
	if caller.Empty() {
		return nil, ErrNoCallerIdentity
	}

	key := cacheKey(caller)
	if hit, ok := r.cacheLookup(key); ok {
		return hit.memberships, nil
	}

	// Single-flight the cold path so concurrent callers on the same key
	// share one upstream round-trip instead of stampeding Grafana.
	//
	// DoChan rather than Do so per-caller context cancellation is
	// independent: with Do the FIRST caller's ctx.Cancel propagates the
	// resulting context.Canceled to every waiter. DoChan + select lets
	// each waiter watch its own ctx and only the leader's load is
	// actually shared. The leader detaches from the group when its
	// caller bails (Forget) so the next concurrent caller doesn't
	// inherit the cancelled state.
	ch := r.sf.DoChan(key, func() (any, error) {
		// Use a context.Background-derived ctx so a leader-side
		// cancellation by an early-bailing caller doesn't kill the
		// shared upstream call for the others. Bound it with the same
		// timeout the caller would have applied — for tests/dev
		// without a deadline, fall back to an unbounded background.
		loadCtx := context.WithoutCancel(ctx)
		return r.load(loadCtx, caller, key)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			r.sf.Forget(key)
			return nil, res.Err
		}
		return res.Val.(cacheEntry).memberships, nil
	case <-ctx.Done():
		// Leader continues — Forget so a future call doesn't get this
		// cancelled context-derived state.
		r.sf.Forget(key)
		return nil, ctx.Err()
	}
}

// projectMemberships joins per-caller Grafana memberships against the
// current registry list. Returns the caller's accessible orgs (keyed
// by Organization.Name, with Role filled in) plus the lowercased set
// of every {Name, DisplayName} in the registry — used by RequireOrg to
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
		// Role==RoleNone never makes it into memberships (load filters).
		// Defensive guard anyway, since memberships is map-valued and a
		// future code path could insert.
		if role == RoleNone {
			continue
		}
		org, ok := byOrgID[orgID]
		if !ok {
			// Grafana knows about this org but the registry doesn't —
			// org was deleted (or never registered). Skip silently:
			// callers should never see non-registered orgs.
			continue
		}
		org.Role = role
		access[org.Name] = org
	}
	return access, allRefs
}

// load does the per-caller upstream work: ask Grafana for the user and
// their org memberships, then cache OrgID → Role with the appropriate
// positive-or-negative TTL. The registry-side join happens at the per-
// call site (projectMemberships) — load doesn't see Organization values.
func (r *authorizer) load(ctx context.Context, caller Caller, key string) (cacheEntry, error) {
	user, err := r.grafana.LookupUser(ctx, caller.Identity())
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana lookup: %w", err)
	}

	if user == nil {
		// User exists in our IdP but has never logged into Grafana yet —
		// we genuinely don't know what orgs they have. Cache nil
		// memberships under the negative TTL: the caller hits a brief
		// "log into Grafana first" window, not the full positive
		// window. Distinct from len(memberships)==0 (user found, in
		// no orgs) — same TTL but different semantics.
		entry := cacheEntry{
			expiresAt:   time.Now().Add(r.negativeCacheTTL),
			memberships: nil,
		}
		r.cacheStore(key, entry)
		return entry, nil
	}

	rawMemberships, err := r.grafana.UserOrgs(ctx, user.ID)
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

	ttl := r.cacheTTL
	if len(memberships) == 0 {
		// User found in Grafana but has no role anywhere we recognise —
		// negative cache so a freshly-provisioned user doesn't wait
		// the full positive window.
		ttl = r.negativeCacheTTL
	}
	entry := cacheEntry{
		expiresAt:   time.Now().Add(ttl),
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
// authz boundary. The returned value aliases cache-owned slices —
// callers that hand the result to external code must clone via
// cloneOrganization.
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
