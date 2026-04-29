package authz

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/authz")

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
// Caller is derived from ctx via CallerFromContext; an unauthenticated
// Caller (no Subject) returns ErrNoCallerIdentity.
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
// A caller who authenticated with our IdP but has not yet logged into
// Grafana surfaces ErrCallerUnknownToGrafana — list_orgs translates that
// to an empty list with a guidance message.
//
// The registry list is read fresh every call — only the per-caller
// Grafana role assignments are cached. A GrafanaOrganization deletion takes
// effect immediately for every caller, not after their TTL expires.
func (a *authorizer) ListOrgs(ctx context.Context) (map[string]Organization, error) {
	roles, err := a.resolveRoles(ctx, CallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	orgs, err := a.orgs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	return cloneOrganizations(accessibleOrgs(roles, orgs)), nil
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
	// Span lets trace waterfalls show authz cost vs handler cost without
	// an extra slog hop; attrs are set on success too so a slow RequireOrg
	// shows the org without grepping the audit log.
	ctx, span := tracer.Start(ctx, "authz.RequireOrg")
	span.SetAttributes(
		attribute.String("authz.org_ref", orgRef),
		attribute.String("authz.min_role", minRole.String()),
	)
	defer span.End()

	if minRole == RoleNone {
		span.SetStatus(codes.Error, ErrInvalidMinRole.Error())
		return Organization{}, ErrInvalidMinRole
	}
	roles, err := a.resolveRoles(ctx, CallerFromContext(ctx))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Organization{}, err
	}
	orgs, err := a.orgs.List(ctx)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Organization{}, fmt.Errorf("list orgs: %w", err)
	}
	access := accessibleOrgs(roles, orgs)

	org, ok, err := findOrganization(access, orgRef)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Organization{}, err
	}
	if ok {
		if !org.Role.AtLeast(minRole) {
			err := insufficientRoleFor(orgRef, org.Role, minRole)
			span.SetStatus(codes.Error, err.Error())
			return Organization{}, err
		}
		return cloneOrganization(org), nil
	}

	// Distinguish "org doesn't exist" from "caller has no role on it"
	// without a second upstream call.
	target := strings.ToLower(orgRef)
	for _, o := range orgs {
		if strings.ToLower(o.Name) == target || strings.ToLower(o.DisplayName) == target {
			err := notAuthorisedFor(orgRef)
			span.SetStatus(codes.Error, err.Error())
			return Organization{}, err
		}
	}
	err = fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	span.SetStatus(codes.Error, err.Error())
	return Organization{}, err
}

// resolveRoles returns the caller's per-org Role assignments
// (OrgID → Role) as observed by Grafana. Hits the per-caller cache before
// falling back to load(). Returns ErrNoCallerIdentity for an
// unauthenticated caller and ErrCallerUnknownToGrafana when Grafana has no
// user record yet.
func (a *authorizer) resolveRoles(ctx context.Context, caller Caller) (map[int64]Role, error) {
	if !caller.Authenticated() {
		return nil, ErrNoCallerIdentity
	}
	if hit, ok := a.cacheLookup(caller.Subject); ok {
		if hit.status == statusUnknownToGrafana {
			return nil, ErrCallerUnknownToGrafana
		}
		return hit.roles, nil
	}
	entry, err := a.load(ctx, caller)
	if err != nil {
		return nil, err
	}
	if entry.status == statusUnknownToGrafana {
		return nil, ErrCallerUnknownToGrafana
	}
	return entry.roles, nil
}

// accessibleOrgs joins Grafana's per-caller role assignments against the
// current registry list, returning the orgs the caller can act on (keyed
// by Organization.Name, with Role filled in).
//
// Grafana evaluated org_mapping at the caller's last login; the role map
// arrives from /api/users/{id}/orgs. We just hydrate it with our local
// metadata (tenants, datasources).
func accessibleOrgs(roles map[int64]Role, orgs []Organization) map[string]Organization {
	access := make(map[string]Organization, len(roles))
	if len(roles) == 0 {
		return access
	}
	byOrgID := make(map[int64]Organization, len(orgs))
	for _, o := range orgs {
		byOrgID[o.OrgID] = o
	}
	for orgID, role := range roles {
		org, ok := byOrgID[orgID]
		if !ok {
			// Grafana knows about this org but the registry doesn't —
			// org was deleted (or never registered). Skip silently.
			continue
		}
		org.Role = role
		access[org.Name] = org
	}
	return access
}

// load asks Grafana for the user and their org roles, then caches OrgID →
// Role with the appropriate positive-or-negative TTL.
func (a *authorizer) load(ctx context.Context, caller Caller) (cacheEntry, error) {
	user, err := a.grafana.LookupUser(ctx, caller.Identity())
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana lookup: %w", err)
	}
	if user == nil {
		// User exists in our IdP but has never logged into Grafana yet —
		// negative-cache so they hit a brief "log into Grafana first"
		// window, not the full positive window.
		return a.cacheStore(caller.Subject, statusUnknownToGrafana, nil), nil
	}
	entries, err := a.grafana.UserOrgs(ctx, user.ID)
	if err != nil {
		return cacheEntry{}, fmt.Errorf("grafana user orgs: %w", err)
	}
	roles := make(map[int64]Role, len(entries))
	for _, e := range entries {
		role := roleFromGrafana(e.Role)
		if role == RoleNone {
			continue
		}
		roles[e.OrgID] = role
	}
	return a.cacheStore(caller.Subject, statusKnown, roles), nil
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
