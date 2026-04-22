// Package authz resolves a caller's identity to the set of Grafana
// organisations and role they may access.
//
// Grafana is the source of truth. observability-operator writes an
// org_mapping string to Grafana's SSO settings, and Grafana itself evaluates
// that mapping at each user login to compute per-user (org -> role).
// This package asks Grafana "what orgs does caller X have, and in what role?"
// via /api/users/lookup + /api/users/{id}/orgs, then enriches each result
// with tenant/datasource metadata from the matching GrafanaOrganization CR.
//
// Falling back to CR RBAC evaluation on the MCP side would re-implement
// Grafana's semantics (group matching, "*" wildcard, precedence, casing) and
// drift over time. By deferring to Grafana we inherit whatever mapping logic
// Grafana ships today and whatever it ships tomorrow.
package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Role encodes a caller's permission level within a single Grafana org.
// Ordered so higher-privilege > lower-privilege numerically.
type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleEditor
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleAdmin:
		return "admin"
	default:
		return "none"
	}
}

// MarshalJSON serialises a Role as its lowercase string form so callers can
// embed OrgAccess values directly into tool/resource payloads.
func (r Role) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// AtLeast reports whether r is at least as privileged as other. Prefer this
// over a direct `r < other` comparison — the iota ordering is invisible
// contract that would break on a reorder.
func (r Role) AtLeast(other Role) bool { return r >= other }

// roleFromGrafana converts Grafana's role strings ("Admin", "Editor",
// "Viewer", "None") into our enum. Unknown values map to RoleNone so callers
// never get elevated by accident on a Grafana change.
func roleFromGrafana(s string) Role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "admin", "grafana admin":
		return RoleAdmin
	case "editor":
		return RoleEditor
	case "viewer":
		return RoleViewer
	default:
		return RoleNone
	}
}

// OrgAccess represents a caller's authorised access to one Grafana org.
// Fields carry JSON tags so this struct can be marshaled directly into MCP
// tool and resource responses.
type OrgAccess struct {
	Name        string                     `json:"name"`
	DisplayName string                     `json:"displayName"`
	OrgID       int64                      `json:"orgID"`
	Role        Role                       `json:"role"`
	Tenants     []obsv1alpha2.TenantConfig `json:"tenants"`
	Datasources []obsv1alpha2.DataSource   `json:"datasources"`
}

// HasTenantType returns true if any tenant on this org supports the given type
// (e.g. "alerting" or "data"). Used to guard alerting-only tools.
func (o OrgAccess) HasTenantType(want obsv1alpha2.TenantType) bool {
	for _, t := range o.Tenants {
		if slices.Contains(t.Types, want) {
			return true
		}
	}
	return false
}

// FindDatasourceID picks the first datasource whose name (case-insensitively)
// contains all the given substrings. Returns (0, false) if none match.
// Used by tools to select the Mimir/Loki/Tempo/Alertmanager datasource
// without hard-coding IDs.
func (o OrgAccess) FindDatasourceID(mustContain ...string) (int64, bool) {
	for _, ds := range o.Datasources {
		lower := strings.ToLower(ds.Name)
		match := true
		for _, needle := range mustContain {
			if !strings.Contains(lower, strings.ToLower(needle)) {
				match = false
				break
			}
		}
		if match {
			return ds.ID, true
		}
	}
	return 0, false
}

// GrafanaOrgLookup is the subset of grafana.Client the resolver needs. Kept
// as an interface so tests can stub it and so authz doesn't import grafana.
type GrafanaOrgLookup interface {
	// LookupUserID returns Grafana's internal user id for the given email or
	// login, or (0, false, nil) if the user hasn't been provisioned yet.
	LookupUserID(ctx context.Context, loginOrEmail string) (id int64, found bool, err error)

	// UserOrgs returns the (orgID, roleString) pairs Grafana has computed
	// for the given user. roleString is one of "Admin" | "Editor" | "Viewer"
	// | "None" (Grafana's own strings).
	UserOrgs(ctx context.Context, userID int64) ([]Membership, error)
}

// Membership is the authz-internal projection of a Grafana org membership.
type Membership struct {
	OrgID int64
	Role  string // Grafana's role string
}

// Cache defaults. Positive entries are cached `CacheTTL`; negative entries
// (user-not-found, empty-memberships) use `NegativeCacheTTL`. Negative TTL
// is deliberately short so a mid-SSO-outage failed lookup doesn't lock a
// user out for the full positive window.
const (
	DefaultCacheTTL         = 30 * time.Second
	DefaultNegativeCacheTTL = 5 * time.Second
	DefaultCacheSize        = 10000
)

// Sentinel errors for callers that want to distinguish "this org doesn't
// exist" from "this org exists but you can't access it."
var (
	// ErrOrgNotFound means no GrafanaOrganization CR matches the orgRef
	// (neither by Name nor by DisplayName).
	ErrOrgNotFound = errors.New("org not found")

	// ErrNoCallerIdentity means the resolver was called without any of
	// Email, Subject set on Caller.
	ErrNoCallerIdentity = errors.New("no caller identity")
)

// Resolver answers "what can this caller do?" by asking Grafana for the
// caller's org memberships and joining them against local CR metadata.
//
// The cache is keyed on OIDC subject (stable, non-spoofable). Email can
// change, be unverified, or be re-owned; subject cannot. Email is still
// used as the Grafana lookup input — Grafana stores users by email.
//
// Concurrent callers on a cold key share one upstream round-trip via
// singleflight; the LRU bounds long-running-process memory to
// CacheSize entries. Positive and negative entries carry different TTLs.
type Resolver struct {
	reader  ctrlclient.Reader
	grafana GrafanaOrgLookup
	log     *slog.Logger

	cache            *lru.Cache[string, cachedAccess]
	sf               singleflight.Group
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
}

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

// NewResolver constructs a Resolver with the given cache settings. Passing
// zero for any of the three cache parameters uses the DefaultCache*
// constants. cacheSize of -1 disables caching entirely (useful for tests).
func NewResolver(reader ctrlclient.Reader, grafana GrafanaOrgLookup, log *slog.Logger, cacheTTL, negativeCacheTTL time.Duration, cacheSize int) (*Resolver, error) {
	if cacheTTL == 0 {
		cacheTTL = DefaultCacheTTL
	}
	if negativeCacheTTL == 0 {
		negativeCacheTTL = DefaultNegativeCacheTTL
	}
	if cacheSize == 0 {
		cacheSize = DefaultCacheSize
	}
	r := &Resolver{
		reader:           reader,
		grafana:          grafana,
		log:              log,
		cacheTTL:         cacheTTL,
		negativeCacheTTL: negativeCacheTTL,
	}
	if cacheSize > 0 {
		c, err := lru.New[string, cachedAccess](cacheSize)
		if err != nil {
			return nil, fmt.Errorf("resolver cache: %w", err)
		}
		r.cache = c
	}
	return r, nil
}

// Resolve returns the caller's authorised orgs + role by asking Grafana and
// enriching with CR metadata.
func (r *Resolver) Resolve(ctx context.Context, caller Caller) (map[string]OrgAccess, error) {
	entry, err := r.resolveWithCRs(ctx, caller)
	if err != nil {
		return nil, err
	}
	return cloneAccessMap(entry.access), nil
}

// resolveWithCRs is the internal variant that also returns the set of all
// CR names (for Require's error-disambiguation). Callers outside this
// package should use Resolve.
func (r *Resolver) resolveWithCRs(ctx context.Context, caller Caller) (cachedAccess, error) {
	if caller.Empty() {
		return cachedAccess{}, ErrNoCallerIdentity
	}

	key := cacheKey(caller)

	// Serve fresh entries straight from the LRU.
	if r.cache != nil {
		if hit, ok := r.cache.Get(key); ok && time.Now().Before(hit.expiresAt) {
			return hit, nil
		}
	}

	// Single-flight the cold path so concurrent callers on the same key
	// share one upstream round-trip instead of stampeding Grafana.
	v, err, _ := r.sf.Do(key, func() (any, error) {
		return r.load(ctx, caller, key)
	})
	if err != nil {
		return cachedAccess{}, err
	}
	return v.(cachedAccess), nil
}

// load does the actual upstream work for one cache key: Grafana lookup,
// Grafana user-orgs, CR list, and CR-to-org join. Caches the result with
// the appropriate positive-or-negative TTL.
func (r *Resolver) load(ctx context.Context, caller Caller, key string) (cachedAccess, error) {
	userID, found, err := r.grafana.LookupUserID(ctx, caller.Identity())
	if err != nil {
		return cachedAccess{}, fmt.Errorf("grafana lookup: %w", err)
	}

	var list obsv1alpha2.GrafanaOrganizationList
	if err := r.reader.List(ctx, &list); err != nil {
		return cachedAccess{}, fmt.Errorf("list GrafanaOrganization: %w", err)
	}
	allOrgRefs := buildOrgRefSet(&list)

	if !found {
		// User exists in our IdP but has never logged into Grafana yet — we
		// genuinely don't know what orgs they have. Return empty + cache
		// briefly so the MCP tells them "log into Grafana first" without
		// locking them out for the full positive window.
		entry := cachedAccess{
			expiresAt:  time.Now().Add(r.negativeCacheTTL),
			access:     map[string]OrgAccess{},
			allOrgRefs: allOrgRefs,
		}
		r.store(key, entry)
		return entry, nil
	}

	memberships, err := r.grafana.UserOrgs(ctx, userID)
	if err != nil {
		return cachedAccess{}, fmt.Errorf("grafana user orgs: %w", err)
	}

	byOrgID := make(map[int64]*obsv1alpha2.GrafanaOrganization, len(list.Items))
	for i := range list.Items {
		byOrgID[list.Items[i].Status.OrgID] = &list.Items[i]
	}

	out := make(map[string]OrgAccess, len(memberships))
	for _, m := range memberships {
		role := roleFromGrafana(m.Role)
		if role == RoleNone {
			continue
		}
		cr, ok := byOrgID[m.OrgID]
		if !ok {
			// Grafana knows about this org but no matching CR — skip. This
			// shouldn't happen in a properly-operated cluster but is safe to
			// ignore rather than leaking non-CR orgs.
			continue
		}
		out[cr.Name] = toOrgAccess(cr, role)
	}

	ttl := r.cacheTTL
	if len(out) == 0 {
		// Empty result is a negative — user exists in Grafana but has no
		// orgs we can show. Cache briefly.
		ttl = r.negativeCacheTTL
	}
	entry := cachedAccess{
		expiresAt:  time.Now().Add(ttl),
		access:     out,
		allOrgRefs: allOrgRefs,
	}
	r.store(key, entry)
	return entry, nil
}

// Require returns the caller's access to orgRef, erroring if the org
// doesn't exist (ErrOrgNotFound), the caller isn't authorised for it
// (ErrNotAuthorised), or their role is below minRole.
//
// orgRef may be either the CR name or the spec.displayName
// (case-insensitive).
func (r *Resolver) Require(ctx context.Context, caller Caller, orgRef string, minRole Role) (OrgAccess, error) {
	entry, err := r.resolveWithCRs(ctx, caller)
	if err != nil {
		return OrgAccess{}, err
	}

	if oa, ok := findAccess(entry.access, orgRef); ok {
		if !oa.Role.AtLeast(minRole) {
			return OrgAccess{}, ErrInsufficientRole(orgRef, oa.Role, minRole)
		}
		return cloneOrgAccess(oa), nil
	}

	// The caller doesn't have access. Disambiguate "org doesn't exist" vs
	// "caller not a member" using the CR set we already cached — no extra
	// List call needed.
	if _, knownOrg := entry.allOrgRefs[strings.ToLower(orgRef)]; !knownOrg {
		return OrgAccess{}, fmt.Errorf("%w: %q", ErrOrgNotFound, orgRef)
	}
	return OrgAccess{}, ErrNotAuthorised(orgRef)
}

// findAccess locates an OrgAccess by CR name (exact) or by DisplayName
// (case-insensitive). Returns the found entry and true, or zero and false.
func findAccess(access map[string]OrgAccess, orgRef string) (OrgAccess, bool) {
	if oa, ok := access[orgRef]; ok {
		return oa, true
	}
	target := strings.ToLower(orgRef)
	for _, oa := range access {
		if strings.ToLower(oa.DisplayName) == target {
			return oa, true
		}
	}
	return OrgAccess{}, false
}

// store writes an entry to the LRU. No-op when caching is disabled
// (cacheSize < 0 at construction time).
func (r *Resolver) store(key string, entry cachedAccess) {
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

// buildOrgRefSet returns the set of lowercased CR Name + DisplayName
// values, used by Require to disambiguate "org not found" vs "not
// authorised" without a second List call.
func buildOrgRefSet(list *obsv1alpha2.GrafanaOrganizationList) map[string]struct{} {
	out := make(map[string]struct{}, len(list.Items)*2)
	for i := range list.Items {
		out[strings.ToLower(list.Items[i].Name)] = struct{}{}
		if dn := list.Items[i].Spec.DisplayName; dn != "" {
			out[strings.ToLower(dn)] = struct{}{}
		}
	}
	return out
}

// cloneOrgAccess returns a shallow copy with Tenants + Datasources cloned
// so handler mutations don't escape into the cache. Named fields are
// value-types or immutable strings; only the two slices need attention.
func cloneOrgAccess(oa OrgAccess) OrgAccess {
	oa.Tenants = slices.Clone(oa.Tenants)
	oa.Datasources = slices.Clone(oa.Datasources)
	return oa
}

// cloneAccessMap clones every OrgAccess value in the map. Used only by
// Resolve which returns the full map to callers.
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

func toOrgAccess(cr *obsv1alpha2.GrafanaOrganization, role Role) OrgAccess {
	return OrgAccess{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		OrgID:       cr.Status.OrgID,
		Role:        role,
		Tenants:     cr.Spec.Tenants,
		Datasources: cr.Status.DataSources,
	}
}

// Caller carries the identity bits the resolver needs to ask Grafana about
// someone. Email is the human-facing handle Grafana provisions users by;
// Subject is the OIDC sub claim, the stable non-spoofable identifier used
// as the cache key.
type Caller struct {
	Email   string
	Subject string
}

// Identity returns the best handle to pass to /api/users/lookup — Grafana
// stores users by email for OAuth-provisioned accounts, so Email comes
// first; Subject is the last-resort fallback.
func (c Caller) Identity() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Subject
}

// Empty reports whether no identifying fields were set.
func (c Caller) Empty() bool { return c.Email == "" && c.Subject == "" }

// ErrNotAuthorised signals that Grafana does not grant the caller access to
// the referenced org.
func ErrNotAuthorised(org string) error {
	return fmt.Errorf("not authorised for org %q", org)
}

// ErrInsufficientRole signals the caller has access but below the required role.
func ErrInsufficientRole(org string, have, need Role) error {
	return fmt.Errorf("insufficient role for org %q: have %s, need %s", org, have, need)
}
