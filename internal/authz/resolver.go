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
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// Resolver answers "what can this caller do?" by asking Grafana for the
// caller's org memberships and joining them against local CR metadata.
type Resolver struct {
	reader  ctrlclient.Reader
	grafana GrafanaOrgLookup
	log     *slog.Logger

	cacheTTL time.Duration
	cacheMu  sync.RWMutex
	cache    map[string]cachedAccess // key: lowercased email/login
}

type cachedAccess struct {
	at    time.Time
	value map[string]OrgAccess
}

// NewResolver constructs a Resolver backed by the given CR reader and Grafana
// client. cacheTTL controls how long per-caller org lookups are cached
// in-process (typical: 30-60s). Pass 0 to disable caching.
func NewResolver(reader ctrlclient.Reader, grafana GrafanaOrgLookup, log *slog.Logger, cacheTTL time.Duration) *Resolver {
	return &Resolver{
		reader:   reader,
		grafana:  grafana,
		log:      log,
		cacheTTL: cacheTTL,
		cache:    map[string]cachedAccess{},
	}
}

// Resolve returns the caller's authorised orgs + role by asking Grafana and
// enriching with CR metadata. The `groups` parameter is accepted for API
// compatibility but ignored — Grafana evaluates groups itself.
func (r *Resolver) Resolve(ctx context.Context, caller Caller) (map[string]OrgAccess, error) {
	if caller.Empty() {
		return nil, fmt.Errorf("no caller identity")
	}

	key := strings.ToLower(caller.Identity())
	if r.cacheTTL > 0 {
		r.cacheMu.RLock()
		if hit, ok := r.cache[key]; ok && time.Since(hit.at) < r.cacheTTL {
			r.cacheMu.RUnlock()
			return hit.value, nil
		}
		r.cacheMu.RUnlock()
	}

	userID, found, err := r.grafana.LookupUserID(ctx, caller.Identity())
	if err != nil {
		return nil, fmt.Errorf("grafana lookup: %w", err)
	}
	if !found {
		// User exists in our IdP but has never logged into Grafana yet — we
		// genuinely don't know what orgs they have. Return empty + cache
		// briefly so the MCP tells them "log into Grafana first".
		out := map[string]OrgAccess{}
		r.store(key, out)
		return out, nil
	}

	memberships, err := r.grafana.UserOrgs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("grafana user orgs: %w", err)
	}
	if len(memberships) == 0 {
		out := map[string]OrgAccess{}
		r.store(key, out)
		return out, nil
	}

	// Load CRs once, index by orgID — cheap (cache-backed list).
	var list obsv1alpha2.GrafanaOrganizationList
	if err := r.reader.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list GrafanaOrganization: %w", err)
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
	r.store(key, out)
	return out, nil
}

// Require returns the caller's access to orgRef, erroring if they don't have
// it or their role is below minRole. orgRef may be either the CR name or the
// spec.displayName (case-insensitive).
func (r *Resolver) Require(ctx context.Context, caller Caller, orgRef string, minRole Role) (OrgAccess, error) {
	access, err := r.Resolve(ctx, caller)
	if err != nil {
		return OrgAccess{}, err
	}
	// Match by CR name first.
	if oa, ok := access[orgRef]; ok {
		if oa.Role < minRole {
			return OrgAccess{}, ErrInsufficientRole(orgRef, oa.Role, minRole)
		}
		return oa, nil
	}
	// Fall back to displayName (case-insensitive).
	target := strings.ToLower(orgRef)
	for _, oa := range access {
		if strings.ToLower(oa.DisplayName) == target {
			if oa.Role < minRole {
				return OrgAccess{}, ErrInsufficientRole(orgRef, oa.Role, minRole)
			}
			return oa, nil
		}
	}
	// The caller might simply not have access, OR the org name might be
	// wrong. Disambiguate by checking if the CR exists at all.
	if _, err := r.lookupCR(ctx, orgRef); err == nil {
		return OrgAccess{}, ErrNotAuthorised(orgRef)
	}
	return OrgAccess{}, ErrNotAuthorised(orgRef)
}

func (r *Resolver) store(key string, v map[string]OrgAccess) {
	if r.cacheTTL <= 0 {
		return
	}
	r.cacheMu.Lock()
	r.cache[key] = cachedAccess{at: time.Now(), value: v}
	r.cacheMu.Unlock()
}

// lookupCR is used only for error-disambiguation in Require. The happy path
// reads CRs in Resolve.
func (r *Resolver) lookupCR(ctx context.Context, orgRef string) (*obsv1alpha2.GrafanaOrganization, error) {
	var cr obsv1alpha2.GrafanaOrganization
	if err := r.reader.Get(ctx, ctrlclient.ObjectKey{Name: orgRef}, &cr); err == nil {
		return &cr, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	var list obsv1alpha2.GrafanaOrganizationList
	if err := r.reader.List(ctx, &list); err != nil {
		return nil, err
	}
	target := strings.ToLower(orgRef)
	for i := range list.Items {
		if strings.ToLower(list.Items[i].Spec.DisplayName) == target {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(obsv1alpha2.GroupVersion.WithResource("grafanaorganizations").GroupResource(), orgRef)
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
// someone. Prefer Email when available (Grafana stores users by email for
// OAuth-provisioned accounts); fall back to login/subject.
type Caller struct {
	Email   string
	Login   string
	Subject string
}

// Identity returns the best handle to pass to /api/users/lookup.
func (c Caller) Identity() string {
	if c.Email != "" {
		return c.Email
	}
	if c.Login != "" {
		return c.Login
	}
	return c.Subject
}

// Empty reports whether no identifying fields were set.
func (c Caller) Empty() bool { return c.Email == "" && c.Login == "" && c.Subject == "" }

// ErrNotAuthorised signals that Grafana does not grant the caller access to
// the referenced org.
func ErrNotAuthorised(org string) error {
	return fmt.Errorf("not authorised for org %q", org)
}

// ErrInsufficientRole signals the caller has access but below the required role.
func ErrInsufficientRole(org string, have, need Role) error {
	return fmt.Errorf("insufficient role for org %q: have %s, need %s", org, have, need)
}
