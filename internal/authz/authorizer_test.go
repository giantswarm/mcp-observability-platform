package authz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// newOrg builds an Organization fixture. tenantTypes populates the single
// tenant's Types; Datasources include Mimir/Loki entries named after the org
// so FindDatasourceID tests have realistic matches.
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
		Datasources: []Datasource{
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

// mustNewAuthorizer constructs an Authorizer with default TTLs and the given
// cache size, and fails the test if construction errors. Pass cacheSize=-1
// to disable caching (uncached read-through every call) — the most common
// shape for tests that assert upstream-call counts.
func mustNewAuthorizer(t *testing.T, reg OrgRegistry, g grafana.Client, cacheSize int) Authorizer {
	t.Helper()
	res, err := NewAuthorizer(reg, g, nil, 0, 0, cacheSize)
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
	calls          struct{ lookup, userOrgs int }
}

// lookupUserReturn is the precise shape grafana.Client.LookupUser returns
// (an anonymous struct pointer); declared as a named type so the test
// stubs can return it without repeating the literal every time.
type lookupUserReturn = struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Login string `json:"login"`
}

func (f *fakeGrafana) LookupUser(_ context.Context, loginOrEmail string) (*lookupUserReturn, error) {
	f.calls.lookup++
	id, ok := f.users[loginOrEmail]
	if !ok {
		return nil, nil // not yet provisioned in Grafana
	}
	return &lookupUserReturn{ID: id, Email: loginOrEmail}, nil
}

func (f *fakeGrafana) UserOrgs(_ context.Context, userID int64) ([]grafana.UserOrgMembership, error) {
	f.calls.userOrgs++
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
	r := mustNewAuthorizer(t, registry(alpha, beta), g, -1)

	got, err := r.ListOrgs(context.Background(), Caller{Email: "u@example.com"})
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
	r := mustNewAuthorizer(t, registry(alpha), g, -1)

	got, _ := r.ListOrgs(context.Background(), Caller{Email: "u@e.com"})
	if len(got) != 0 {
		t.Errorf("expected empty access, got %v", got)
	}
}

func TestAuthorizer_Resolve_UserNeverLoggedIn(t *testing.T) {
	// Grafana returns 404 on lookup → found=false → resolver returns empty
	// without erroring, so the UX is "no access yet; log into Grafana first".
	g := &fakeGrafana{users: map[string]int64{} /* empty */}
	r := mustNewAuthorizer(t, registry(), g, -1)

	got, err := r.ListOrgs(context.Background(), Caller{Email: "new@e.com"})
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
	r := mustNewAuthorizer(t, registry(alpha), g, 100)

	_, _ = r.ListOrgs(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.ListOrgs(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.ListOrgs(context.Background(), Caller{Email: "u@e.com"})
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
	r := mustNewAuthorizer(t, registry(alpha), g, -1)

	_, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleAdmin)
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
	r := mustNewAuthorizer(t, registry(alpha), g, -1)

	_, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleViewer)
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
	r := mustNewAuthorizer(t, registry(alpha), g, -1)

	org, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com"}, "ALPHA TEAM", RoleAdmin)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if org.Name != "alpha" {
		t.Fatalf("want name=alpha, got %q", org.Name)
	}
}

func TestRoleFromGrafana(t *testing.T) {
	cases := map[string]Role{
		"Admin":         RoleAdmin,
		"admin":         RoleAdmin,
		"Grafana Admin": RoleAdmin,
		"Editor":        RoleEditor,
		"Viewer":        RoleViewer,
		"None":          RoleNone,
		"":              RoleNone,
		"weird":         RoleNone, // unknown -> deny
	}
	for in, want := range cases {
		if got := roleFromGrafana(in); got != want {
			t.Errorf("roleFromGrafana(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOrganization_FindDatasourceID(t *testing.T) {
	org := Organization{
		Datasources: []Datasource{
			{ID: 1, Name: "mimir-prod"},
			{ID: 2, Name: "Loki-Prod"},
			{ID: 3, Name: "tempo-prod"},
		},
	}
	cases := []struct {
		need   []string
		wantID int64
		wantOK bool
	}{
		{[]string{"mimir"}, 1, true},
		{[]string{"loki"}, 2, true},
		{[]string{"tempo", "prod"}, 3, true},
		{[]string{"prometheus"}, 0, false},
		{[]string{"mimir", "nope"}, 0, false},
	}
	for _, c := range cases {
		gotID, gotOK := org.FindDatasourceID(c.need...)
		if gotID != c.wantID || gotOK != c.wantOK {
			t.Errorf("FindDatasourceID(%v) = (%d,%v), want (%d,%v)", c.need, gotID, gotOK, c.wantID, c.wantOK)
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

// blockingGrafana lets tests gate LookupUser on a channel so concurrent
// callers are guaranteed to arrive at the cache miss together. Counts both
// LookupUser and UserOrgs calls atomically under -race. Embeds
// grafana.Client (nil) so any method we don't override panics if called.
type blockingGrafana struct {
	grafana.Client // nil — unused methods panic
	users          map[string]int64
	orgs           map[int64][]grafana.UserOrgMembership
	lookupCalls    atomic.Int32
	userOrgs       atomic.Int32
	release        chan struct{}
}

func (b *blockingGrafana) LookupUser(_ context.Context, loginOrEmail string) (*lookupUserReturn, error) {
	b.lookupCalls.Add(1)
	if b.release != nil {
		<-b.release
	}
	id, ok := b.users[loginOrEmail]
	if !ok {
		return nil, nil
	}
	return &lookupUserReturn{ID: id, Email: loginOrEmail}, nil
}

func (b *blockingGrafana) UserOrgs(_ context.Context, userID int64) ([]grafana.UserOrgMembership, error) {
	b.userOrgs.Add(1)
	return b.orgs[userID], nil
}

// TestAuthorizer_Singleflight_CollapsesConcurrentCallers proves that N
// concurrent callers on the same cold cache key do exactly ONE upstream
// round-trip, not N. Guards against the "stampede / thundering herd"
// failure mode where a cache expiry under load fans out.
func TestAuthorizer_Singleflight_CollapsesConcurrentCallers(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &blockingGrafana{
		users:   map[string]int64{"u@e.com": 1},
		orgs:    map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Admin"}}},
		release: make(chan struct{}),
	}
	r := mustNewAuthorizer(t, registry(alpha), g, 100)

	const callers = 50
	var started sync.WaitGroup
	started.Add(callers)
	var done sync.WaitGroup
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			started.Done()
			_, err := r.ListOrgs(context.Background(), Caller{Email: "u@e.com", Subject: "sub-1"})
			if err != nil {
				t.Errorf("Resolve: %v", err)
			}
		}()
	}
	started.Wait()
	// Race window: all 50 goroutines are blocked on the singleflight group
	// waiting for one to finish the upstream call. Release the single in-
	// flight caller; the rest share its result.
	close(g.release)
	done.Wait()

	if got := g.lookupCalls.Load(); got != 1 {
		t.Errorf("LookupUserID calls = %d, want 1 (singleflight should collapse %d concurrent callers)", got, callers)
	}
	if got := g.userOrgs.Load(); got != 1 {
		t.Errorf("UserOrgs calls = %d, want 1", got)
	}
}

// TestAuthorizer_CacheKeyIsSubjectNotEmail proves the same Subject under two
// different Email values shares a cache entry. Fixes the spoofability
// concern: email can change or be unverified, subject cannot.
func TestAuthorizer_CacheKeyIsSubjectNotEmail(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@old.com": 1, "u@new.com": 1},
		orgs:  map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewAuthorizer(t, registry(alpha), g, 100)

	// Same subject, different emails — second call should hit cache.
	_, _ = r.ListOrgs(context.Background(), Caller{Email: "u@old.com", Subject: "sub-1"})
	_, _ = r.ListOrgs(context.Background(), Caller{Email: "u@new.com", Subject: "sub-1"})
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
	r := mustNewAuthorizer(t, registry(alpha), g, 100)

	oa1, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	// Mutation simulating a handler that appends to the datasource list.
	oa1.Datasources = append(oa1.Datasources, Datasource{ID: 999, Name: "poisoned"})

	oa2, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
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
	r := mustNewAuthorizer(t, registry(alpha), g, 100)

	oa1, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	oa1.Tenants[0].Types = append(oa1.Tenants[0].Types, TenantTypeAlerting)

	oa2, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
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
	r := mustNewAuthorizer(t, registry(alpha), g, -1)

	// alpha exists but the caller isn't a member → ErrNotAuthorised.
	_, err := r.RequireOrg(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleViewer)
	if err == nil {
		t.Fatal("expected error for known-org-but-no-access, got nil")
	}
	if errors.Is(err, ErrOrgNotFound) {
		t.Errorf("err = %v, want NOT-ErrOrgNotFound (alpha is in the registry)", err)
	}

	// nonexistent has no descriptor → ErrOrgNotFound.
	_, err = r.RequireOrg(context.Background(), Caller{Email: "u@e.com"}, "nonexistent", RoleViewer)
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

func TestCaller_IdentityAndEmpty(t *testing.T) {
	if !(Caller{}).Empty() {
		t.Error("zero Caller should be Empty")
	}
	if (Caller{Subject: "x"}).Empty() {
		t.Error("with Subject should not be Empty")
	}
	if got := (Caller{Email: "e", Subject: "s"}).Identity(); got != "e" {
		t.Errorf("Identity email-preferred = %q, want e", got)
	}
	if got := (Caller{Subject: "s"}).Identity(); got != "s" {
		t.Errorf("Identity subject-fallback = %q, want s", got)
	}
}

// TestAuthorizer_ConcurrentEviction_NoStaleAuthDecisions stresses the LRU
// under a hot load that forces evictions while concurrent readers are
// resolving. The invariant: a caller's Resolve must always return a map
// whose Role + OrgID reflect that caller's own fakeGrafana state — never
// another caller's state carried across by a cache bug. A regression (e.g.
// a cache that returns on key-prefix match, or an aliased map across
// evictions) surfaces here as "caller A sees caller B's org".
//
// Run with -race. Failures surface as either a race report or an
// assertion mismatch on the caller's expected role/org set.
func TestAuthorizer_ConcurrentEviction_NoStaleAuthDecisions(t *testing.T) {
	const (
		cacheSize = 16
		callers   = 64 // > cacheSize so LRU evicts continuously
		goros     = 32
		rounds    = 200
	)

	// One org per caller; caller i is Admin on org i. If the cache ever
	// returns caller i's entry to caller j, the assertion catches it.
	orgs := make([]Organization, callers)
	users := map[string]int64{}
	orgMap := map[int64][]grafana.UserOrgMembership{}
	for i := 0; i < callers; i++ {
		orgs[i] = newOrg(fmt.Sprintf("org-%d", i), fmt.Sprintf("Org %d", i), int64(i+1))
		email := fmt.Sprintf("u%d@e.com", i)
		users[email] = int64(i + 1)
		orgMap[int64(i+1)] = []grafana.UserOrgMembership{{OrgID: int64(i + 1), Role: "Admin"}}
	}
	// blockingGrafana uses atomic counters so LookupUserID is race-safe
	// under concurrent callers. release==nil means no artificial blocking.
	g := &blockingGrafana{users: users, orgs: orgMap}
	r := mustNewAuthorizer(t, registry(orgs...), g, cacheSize)

	var wg sync.WaitGroup
	wg.Add(goros)
	errs := make(chan error, goros*rounds)
	for w := 0; w < goros; w++ {
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				// Spread load across callers with two large primes so each
				// worker visits the full set but in a different order.
				i := (worker*1315423911 + round*2654435761) % callers
				caller := Caller{
					Email:   fmt.Sprintf("u%d@e.com", i),
					Subject: fmt.Sprintf("sub-%d", i),
				}
				access, err := r.ListOrgs(context.Background(), caller)
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
