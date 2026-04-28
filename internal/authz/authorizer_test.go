package authz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/mcp-oauth/providers"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// ctxWithCaller builds a context carrying the given caller via the
// production withCaller path, so tests exercise the same context-
// propagation as runtime callers. Authorizer methods derive the caller
// from ctx via CallerFromContext, with the framework-level RequireCaller
// middleware already filtering out empty callers before any handler runs.
//
// Subject is required at the production boundary (Caller.Authenticated() rejects
// subjectless callers — see fail-closed change) but most test fixtures
// only care about the email-based Grafana lookup; default Subject from
// Email when the test didn't supply one explicitly.
func ctxWithCaller(c Caller) context.Context {
	if c.Subject == "" && c.Email != "" {
		c.Subject = "sub-" + c.Email
	}
	return withCaller(context.Background(), &providers.UserInfo{Email: c.Email, ID: c.Subject})
}

// newOrg builds an Organization fixture. tenantTypes populates the single
// tenant's Types; Datasources include Mimir/Loki entries named after the org
// so FindDatasource tests have realistic matches.
func newOrg(name, display string, orgID int64, tenantTypes ...TenantType) Organization {
	var tenants []Tenant
	if len(tenantTypes) > 0 {
		tenants = []Tenant{{Name: name, Types: tenantTypes}}
	}
	return Organization{
		Name:        name,
		DisplayName: display,
		OrgID:       orgID,
		Tenants:     tenants,
		Datasources: []grafana.Datasource{
			{ID: 10, Name: "mimir-" + name},
			{ID: 11, Name: "loki-" + name},
		},
	}
}

// fakeRegistry implements OrgRegistry for tests — hand-rolled instead of a
// controller-runtime fake client so tests don't need to know about K8s or
// the CRD package.
type fakeRegistry struct {
	descs []Organization
}

func (f *fakeRegistry) List(context.Context) ([]Organization, error) {
	return f.descs, nil
}

func registry(descs ...Organization) *fakeRegistry {
	return &fakeRegistry{descs: descs}
}

// mustNewAuthorizer constructs an Authorizer with the default TTL and
// fails the test if construction errors. Use mustNewAuthorizerWithTTL
// for tests that need an explicit (typically short) TTL.
func mustNewAuthorizer(t *testing.T, reg OrgRegistry, g grafana.Client) Authorizer {
	t.Helper()
	res, err := NewAuthorizer(reg, g, nil, 0)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	return res
}

// mustNewAuthorizerWithTTL exposes the cache TTL so expiry-window tests
// can use very short values.
func mustNewAuthorizerWithTTL(t *testing.T, reg OrgRegistry, g grafana.Client, ttl time.Duration) Authorizer {
	t.Helper()
	res, err := NewAuthorizer(reg, g, nil, ttl)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	return res
}

// fakeGrafana stubs grafana.Client for authorizer tests. Only LookupUser
// and UserOrgs are called by the production code path; the embedded
// grafana.Client is nil, so any other method blows up with a clear nil-
// receiver panic — exactly what we want if the authorizer ever grows a
// new upstream call we haven't accounted for here.
type fakeGrafana struct {
	grafana.Client                                       // nil — unused methods panic
	users          map[string]int64                      // email/login -> id
	orgs           map[int64][]grafana.UserOrgMembership // id -> memberships
	mu             sync.Mutex
	calls          struct{ lookup, userOrgs int }
}

func (f *fakeGrafana) LookupUser(_ context.Context, loginOrEmail string) (*grafana.User, error) {
	f.mu.Lock()
	f.calls.lookup++
	f.mu.Unlock()
	id, ok := f.users[loginOrEmail]
	if !ok {
		return nil, nil // not yet provisioned in Grafana
	}
	return &grafana.User{ID: id, Email: loginOrEmail}, nil
}

func (f *fakeGrafana) UserOrgs(_ context.Context, userID int64) ([]grafana.UserOrgMembership, error) {
	f.mu.Lock()
	f.calls.userOrgs++
	f.mu.Unlock()
	return f.orgs[userID], nil
}

func TestAuthorizer_Resolve_MapsGrafanaRoleStrings(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 42, TenantTypeData)
	beta := newOrg("beta", "Beta", 7, TenantTypeAlerting)
	g := &fakeGrafana{
		users: map[string]int64{"u@example.com": 1},
		orgs: map[int64][]grafana.UserOrgMembership{
			1: {
				{OrgID: 42, Role: "Admin"},
				{OrgID: 7, Role: "Viewer"},
			},
		},
	}
	r := mustNewAuthorizer(t, registry(alpha, beta), g)

	got, err := r.ListOrgs(ctxWithCaller(Caller{Email: "u@example.com"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["alpha"].Role != RoleAdmin {
		t.Errorf("alpha role = %v, want Admin", got["alpha"].Role)
	}
	if got["beta"].Role != RoleViewer {
		t.Errorf("beta role = %v, want Viewer", got["beta"].Role)
	}
}

func TestAuthorizer_Resolve_DropsRoleNoneAndUnknownOrgs(t *testing.T) {
	// Descriptor exists for orgID 42 but not 99. Role "None" also dropped.
	alpha := newOrg("alpha", "Alpha", 42)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 5},
		orgs: map[int64][]grafana.UserOrgMembership{
			5: {
				{OrgID: 42, Role: "None"},  // dropped: no role
				{OrgID: 99, Role: "Admin"}, // dropped: no matching descriptor
			},
		},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	got, _ := r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"}))
	if len(got) != 0 {
		t.Errorf("expected empty access, got %v", got)
	}
}

func TestAuthorizer_Resolve_UserNeverLoggedIn(t *testing.T) {
	// Grafana returns 404 on lookup → found=false → resolver returns empty
	// without erroring, so the UX is "no access yet; log into Grafana first".
	g := &fakeGrafana{users: map[string]int64{} /* empty */}
	r := mustNewAuthorizer(t, registry(), g)

	got, err := r.ListOrgs(ctxWithCaller(Caller{Email: "new@e.com"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestAuthorizer_Resolve_Cache(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	_, _ = r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"}))
	_, _ = r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"}))
	_, _ = r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"}))
	if g.calls.lookup != 1 || g.calls.userOrgs != 1 {
		t.Errorf("expected 1 lookup + 1 userOrgs call, got %d/%d", g.calls.lookup, g.calls.userOrgs)
	}
}

func TestAuthorizer_Require_InsufficientRole(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	_, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "alpha", RoleAdmin)
	if err == nil {
		t.Fatalf("expected insufficient-role error, got nil")
	}
}

func TestAuthorizer_Require_NotAuthorised(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {}}, // user exists but in no orgs
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	_, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "alpha", RoleViewer)
	if err == nil {
		t.Fatalf("expected not-authorised error, got nil")
	}
}

func TestAuthorizer_Require_LookupByDisplayNameCaseInsensitive(t *testing.T) {
	alpha := newOrg("alpha", "Alpha Team", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	org, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "ALPHA TEAM", RoleAdmin)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if org.Name != "alpha" {
		t.Fatalf("want name=alpha, got %q", org.Name)
	}
}

func TestRoleFromGrafana(t *testing.T) {
	cases := map[string]Role{
		"Admin":  RoleAdmin,
		"admin":  RoleAdmin,
		"Editor": RoleEditor,
		"Viewer": RoleViewer,
		"None":   RoleNone,
		"":       RoleNone,
		"weird":  RoleNone, // unknown -> deny
		// "Grafana Admin" is the server-admin role on /api/users/{id},
		// not a per-org role string. roleFromGrafana parses per-org
		// memberships only, so an unrelated string lookup must NOT
		// elevate to RoleAdmin (catches a regression that re-adds the
		// alias).
		"Grafana Admin": RoleNone,
	}
	for in, want := range cases {
		if got := roleFromGrafana(in); got != want {
			t.Errorf("roleFromGrafana(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOrganization_FindDatasource(t *testing.T) {
	org := Organization{
		Datasources: []grafana.Datasource{
			{ID: 1, Name: "mimir-prod"},
			{ID: 2, Name: "Loki-Prod"},
			{ID: 3, Name: "tempo-prod"},
		},
	}
	cases := []struct {
		kind   grafana.DatasourceKind
		wantID int64
		wantOK bool
	}{
		{grafana.DSKindMimir, 1, true},
		{grafana.DSKindLoki, 2, true},
		{grafana.DSKindTempo, 3, true},
		{grafana.DSKindAlertmanager, 0, false},
	}
	for _, c := range cases {
		gotDS, gotOK := org.FindDatasource(c.kind)
		if gotDS.ID != c.wantID || gotOK != c.wantOK {
			t.Errorf("FindDatasource(%v) = (%d,%v), want (%d,%v)", c.kind, gotDS.ID, gotOK, c.wantID, c.wantOK)
		}
	}
}

func TestOrganization_HasTenantType(t *testing.T) {
	org := Organization{
		Tenants: []Tenant{
			{Name: "t1", Types: []TenantType{TenantTypeData}},
			{Name: "t2", Types: []TenantType{TenantTypeAlerting}},
		},
	}
	if !org.HasTenantType(TenantTypeData) {
		t.Error("HasTenantType(data) = false")
	}
	if !org.HasTenantType(TenantTypeAlerting) {
		t.Error("HasTenantType(alerting) = false")
	}
	if org.HasTenantType("nonexistent") {
		t.Error("HasTenantType(nonexistent) = true")
	}
}

func TestRole_MarshalJSON(t *testing.T) {
	cases := map[Role]string{
		RoleNone:   `"none"`,
		RoleViewer: `"viewer"`,
		RoleEditor: `"editor"`,
		RoleAdmin:  `"admin"`,
	}
	for r, want := range cases {
		b, err := r.MarshalJSON()
		if err != nil {
			t.Fatalf("MarshalJSON(%v): %v", r, err)
		}
		if string(b) != want {
			t.Errorf("MarshalJSON(%v) = %s, want %s", r, b, want)
		}
	}
}

//TestAuthorizer_CacheKeyIsSubjectNotEmail proves the same Subject under two
// different Email values shares a cache entry. Fixes the spoofability
// concern: email can change or be unverified, subject cannot.
func TestAuthorizer_CacheKeyIsSubjectNotEmail(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@old.com": 1, "u@new.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	// Same subject, different emails — second call should hit cache.
	_, _ = r.ListOrgs(ctxWithCaller(Caller{Email: "u@old.com", Subject: "sub-1"}))
	_, _ = r.ListOrgs(ctxWithCaller(Caller{Email: "u@new.com", Subject: "sub-1"}))
	if g.calls.lookup != 1 {
		t.Errorf("LookupUserID calls = %d, want 1 (cache keyed on Subject, email change ignored)", g.calls.lookup)
	}
}

// TestAuthorizer_ReturnedSlicesAreCloned proves handler mutations of the
// returned Organization don't escape into the cache. Before the fix, appending
// to oa.Datasources silently corrupted every future cache hit.
func TestAuthorizer_ReturnedSlicesAreCloned(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	oa1, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com", Subject: "s"}), "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	// Mutation simulating a handler that appends to the datasource list.
	oa1.Datasources = append(oa1.Datasources, grafana.Datasource{ID: 999, Name: "poisoned"})

	oa2, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com", Subject: "s"}), "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("second Require: %v", err)
	}
	for _, ds := range oa2.Datasources {
		if ds.ID == 999 {
			t.Fatalf("cache was poisoned by handler-side append (ds=%+v)", ds)
		}
	}
}

// TestAuthorizer_ReturnedTenantTypesAreCloned proves nested-slice mutations
// (oa.Tenants[i].Types) don't escape into the cache either. cloneTenants
// must deep-copy each Tenant's Types slice, not just slices.Clone the outer.
func TestAuthorizer_ReturnedTenantTypesAreCloned(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1, TenantTypeData)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	oa1, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com", Subject: "s"}), "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	oa1.Tenants[0].Types = append(oa1.Tenants[0].Types, TenantTypeAlerting)

	oa2, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com", Subject: "s"}), "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("second Require: %v", err)
	}
	if oa2.HasTenantType(TenantTypeAlerting) {
		t.Fatal("nested Types slice was shared across cache reads")
	}
}

// TestAuthorizer_Require_OrgNotFoundVsNotAuthorised proves that Require
// returns a distinct ErrOrgNotFound when the org simply doesn't exist,
// vs ErrNotAuthorised when the org exists but the caller isn't a member.
// Today both cases are indistinguishable from the caller's side.
func TestAuthorizer_Require_OrgNotFoundVsNotAuthorised(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {}}, // user exists in Grafana but in no orgs
	}
	r := mustNewAuthorizer(t, registry(alpha), g)

	// alpha exists but the caller isn't a member → ErrNotAuthorised.
	_, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "alpha", RoleViewer)
	if err == nil {
		t.Fatal("expected error for known-org-but-no-access, got nil")
	}
	if errors.Is(err, ErrOrgNotFound) {
		t.Errorf("err = %v, want NOT-ErrOrgNotFound (alpha is in the registry)", err)
	}

	// nonexistent has no descriptor → ErrOrgNotFound.
	_, err = r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "nonexistent", RoleViewer)
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("err = %v, want wraps ErrOrgNotFound", err)
	}
}

// TestAuthorizer_Role_AtLeast guards the iota ordering — future reorders
// that would silently break privilege checks fail here.
func TestAuthorizer_Role_AtLeast(t *testing.T) {
	cases := []struct {
		have, need Role
		want       bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleViewer, true},
		{RoleEditor, RoleAdmin, false},
		{RoleViewer, RoleEditor, false},
		{RoleNone, RoleViewer, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.need); got != c.want {
			t.Errorf("(%v).AtLeast(%v) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestCaller_IdentityAndAuthenticated(t *testing.T) {
	if (Caller{}).Authenticated() {
		t.Error("zero Caller should not be Authenticated")
	}
	if !(Caller{Subject: "x"}).Authenticated() {
		t.Error("with Subject should be Authenticated")
	}
	if got := (Caller{Email: "e", Subject: "s"}).Identity(); got != "e" {
		t.Errorf("Identity email-preferred = %q, want e", got)
	}
	if got := (Caller{Subject: "s"}).Identity(); got != "s" {
		t.Errorf("Identity subject-fallback = %q, want s", got)
	}
}

// TestAuthorizer_ConcurrentResolve_NoCallerCacheBleed stresses the cache
// under concurrent readers across many distinct subjects. The invariant:
// a caller's Resolve must return a map whose Role + OrgID reflect that
// caller's own fakeGrafana state — never another caller's state carried
// across by a cache bug (key-prefix match, aliased map, etc.).
//
// Run with -race.
func TestAuthorizer_ConcurrentResolve_NoCallerCacheBleed(t *testing.T) {
	const (
		callers = 64
		goros   = 32
		rounds  = 200
	)

	// One org per caller; caller i is Admin on org i. If the cache ever
	// returns caller i's entry to caller j, the assertion catches it.
	orgs := make([]Organization, callers)
	users := map[string]int64{}
	orgMap := map[int64][]grafana.UserOrgMembership{}
	for i := range callers {
		orgs[i] = newOrg(fmt.Sprintf("org-%d", i), fmt.Sprintf("Org %d", i), int64(i+1))
		email := fmt.Sprintf("u%d@e.com", i)
		users[email] = int64(i + 1)
		orgMap[int64(i+1)] = []grafana.UserOrgMembership{{OrgID: int64(i + 1), Role: "Admin"}}
	}
	g := &fakeGrafana{users: users, orgs: orgMap}
	r := mustNewAuthorizer(t, registry(orgs...), g)

	var wg sync.WaitGroup
	wg.Add(goros)
	errs := make(chan error, goros*rounds)
	for w := range goros {
		go func(worker int) {
			defer wg.Done()
			for round := range rounds {
				// Spread load across callers with two large primes so each
				// worker visits the full set but in a different order.
				i := (worker*1315423911 + round*2654435761) % callers
				caller := Caller{
					Email:   fmt.Sprintf("u%d@e.com", i),
					Subject: fmt.Sprintf("sub-%d", i),
				}
				access, err := r.ListOrgs(ctxWithCaller(caller))
				if err != nil {
					errs <- fmt.Errorf("worker %d round %d: Resolve(%s): %w", worker, round, caller.Email, err)
					return
				}
				if len(access) != 1 {
					errs <- fmt.Errorf("caller %d saw %d orgs, want exactly 1: %v", i, len(access), access)
					return
				}
				wantOrgName := fmt.Sprintf("org-%d", i)
				got, ok := access[wantOrgName]
				if !ok {
					errs <- fmt.Errorf("caller %d missing own org %q; got keys %v", i, wantOrgName, mapKeys(access))
					return
				}
				if got.OrgID != int64(i+1) {
					errs <- fmt.Errorf("caller %d OrgID drift: got %d want %d", i, got.OrgID, i+1)
					return
				}
				if got.Role != RoleAdmin {
					errs <- fmt.Errorf("caller %d role drift: got %v want Admin", i, got.Role)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func mapKeys(m map[string]Organization) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestAuthorizer_PositiveCacheTTL_Expires proves that a positive cache
// entry is re-fetched after the configured TTL elapses. The doc and
// README treat the 30s positive-cache TTL as the freshness guarantee
// for org membership; before this test, no assertion proved it
// actually expires. Uses 20ms TTL + 60ms sleep so the test runs in
// well under 100ms while still leaving headroom against scheduler
// jitter.
func TestAuthorizer_PositiveCacheTTL_Expires(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 42, TenantTypeData)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 42, Role: "Viewer"}}},
	}
	r := mustNewAuthorizerWithTTL(t, registry(alpha), g, 20*time.Millisecond)

	// First call → upstream lookup.
	if _, err := r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"})); err != nil {
		t.Fatalf("ListOrgs#1: %v", err)
	}
	// Second call within TTL → no upstream.
	if _, err := r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"})); err != nil {
		t.Fatalf("ListOrgs#2: %v", err)
	}
	if g.calls.lookup != 1 || g.calls.userOrgs != 1 {
		t.Fatalf("within-TTL: expected 1 lookup + 1 userOrgs, got %d/%d", g.calls.lookup, g.calls.userOrgs)
	}

	// Sleep past TTL → next call should re-fetch.
	time.Sleep(60 * time.Millisecond)
	if _, err := r.ListOrgs(ctxWithCaller(Caller{Email: "u@e.com"})); err != nil {
		t.Fatalf("ListOrgs#3: %v", err)
	}
	if g.calls.lookup != 2 || g.calls.userOrgs != 2 {
		t.Errorf("after TTL: expected 2 lookups + 2 userOrgs, got %d/%d (cache failed to expire)", g.calls.lookup, g.calls.userOrgs)
	}
}

// TestAuthorizer_RegistryDeleteIsImmediate proves M1: a
// GrafanaOrganization removed from the registry is invisible to
// RequireOrg / ListOrgs IMMEDIATELY, not after the per-caller cache
// TTL. The registry list is read fresh on every authz call, so a
// caller with a cached membership for org X cannot still RequireOrg
// it after X is deleted.
func TestAuthorizer_RegistryDeleteIsImmediate(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 42, TenantTypeData)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 42, Role: "Admin"}}},
	}
	reg := registry(alpha)
	// Long TTL so any leaked cached state would survive the delete.
	r := mustNewAuthorizerWithTTL(t, reg, g, time.Hour)

	ctx := ctxWithCaller(Caller{Email: "u@e.com"})
	// Warm the per-caller cache with a positive entry.
	if _, err := r.RequireOrg(ctx, "alpha", RoleViewer); err != nil {
		t.Fatalf("RequireOrg#1: %v", err)
	}

	// Delete the org from the registry. Cache entry for caller still
	// names OrgID=42, but the registry no longer knows about it.
	reg.descs = nil

	if _, err := r.RequireOrg(ctx, "alpha", RoleViewer); err == nil {
		t.Fatalf("RequireOrg#2 after delete: expected error (org not in registry)")
	}
	if _, err := r.RequireOrg(ctx, "alpha", RoleViewer); !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("RequireOrg#3 after delete: err = %v, want wraps ErrOrgNotFound", err)
	}
}

// TestRequireOrg_RejectsRoleNone proves H2: passing RoleNone as
// minRole is a vacuous gate (Role.AtLeast(RoleNone) is always true)
// and must be rejected explicitly so a future contributor can't
// silently bypass authorisation by writing it.
func TestRequireOrg_RejectsRoleNone(t *testing.T) {
	g := &fakeGrafana{users: map[string]int64{"u@e.com": 1}}
	r := mustNewAuthorizer(t, registry(), g)
	_, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "alpha", RoleNone)
	if !errors.Is(err, ErrInvalidMinRole) {
		t.Errorf("err = %v, want wraps ErrInvalidMinRole", err)
	}
}

// TestCallerAuthenticated_RejectsSubjectlessCaller proves the H3 fix:
// a Caller with only an email and no subject is unauthenticated even
// though both fields aren't blank — email is unsafe to use as a cache
// key (mutable in some IdPs, mappable to a different user) so cacheKey
// trusts only Subject.
func TestCallerAuthenticated_RejectsSubjectlessCaller(t *testing.T) {
	if (Caller{Email: "u@e.com"}).Authenticated() {
		t.Error("Caller with email and no Subject must be unauthenticated")
	}
	if (Caller{}).Authenticated() {
		t.Error("zero Caller must be unauthenticated")
	}
	if !(Caller{Subject: "sub-1"}).Authenticated() {
		t.Error("Caller with Subject must be Authenticated")
	}
	if !(Caller{Email: "u@e.com", Subject: "sub-1"}).Authenticated() {
		t.Error("Caller with both Subject and Email must be Authenticated")
	}
}

// TestRequireOrg_AmbiguousDisplayName proves M2: when multiple
// registered orgs share a DisplayName, RequireOrg refuses to silently
// pick one (map iteration is non-deterministic) and returns
// ErrAmbiguousOrgRef so an operator can fix the collision.
func TestRequireOrg_AmbiguousDisplayName(t *testing.T) {
	a := newOrg("a", "Prod", 1, TenantTypeData)
	b := newOrg("b", "Prod", 2, TenantTypeData)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs: map[int64][]grafana.UserOrgMembership{1: {
			{OrgID: 1, Role: "Viewer"},
			{OrgID: 2, Role: "Viewer"},
		}},
	}
	r := mustNewAuthorizer(t, registry(a, b), g)
	_, err := r.RequireOrg(ctxWithCaller(Caller{Email: "u@e.com"}), "Prod", RoleViewer)
	if !errors.Is(err, ErrAmbiguousOrgRef) {
		t.Errorf("err = %v, want wraps ErrAmbiguousOrgRef", err)
	}
}
