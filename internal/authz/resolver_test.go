package authz

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// newDesc builds an OrgDescriptor fixture. tenantTypes populates the single
// tenant's Types; Datasources include Mimir/Loki entries named after the org
// so FindDatasourceID tests have realistic matches.
func newDesc(name, display string, orgID int64, tenantTypes ...TenantType) OrgDescriptor {
	var tenants []Tenant
	if len(tenantTypes) > 0 {
		tenants = []Tenant{{Name: name, Types: tenantTypes}}
	}
	return OrgDescriptor{
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
	descs []OrgDescriptor
}

func (f *fakeRegistry) List(context.Context) ([]OrgDescriptor, error) {
	return f.descs, nil
}

func registry(descs ...OrgDescriptor) *fakeRegistry {
	return &fakeRegistry{descs: descs}
}

// mustNewResolver constructs a Resolver with default TTLs and the given
// cache size, and fails the test if construction errors. Pass cacheSize=-1
// to disable caching (uncached read-through every call) — the most common
// shape for tests that assert upstream-call counts.
func mustNewResolver(t *testing.T, reg OrgRegistry, g OrgMembershipLookup, cacheSize int) *Resolver {
	t.Helper()
	res, err := NewResolver(reg, g, nil, 0, 0, cacheSize)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return res
}

// fakeGrafana is a deterministic in-memory stub of the OrgMembershipLookup
// interface. Tests configure which user IDs exist and what their orgs are.
type fakeGrafana struct {
	users map[string]int64       // email/login -> id
	orgs  map[int64][]Membership // id -> memberships
	calls struct{ lookup, userOrgs int }
}

func (f *fakeGrafana) LookupUserID(_ context.Context, loginOrEmail string) (int64, bool, error) {
	f.calls.lookup++
	id, ok := f.users[loginOrEmail]
	return id, ok, nil
}

func (f *fakeGrafana) UserOrgs(_ context.Context, userID int64) ([]Membership, error) {
	f.calls.userOrgs++
	return f.orgs[userID], nil
}

func TestResolver_Resolve_MapsGrafanaRoleStrings(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 42, TenantTypeData)
	beta := newDesc("beta", "Beta", 7, TenantTypeAlerting)
	g := &fakeGrafana{
		users: map[string]int64{"u@example.com": 1},
		orgs: map[int64][]Membership{
			1: {
				{OrgID: 42, Role: "Admin"},
				{OrgID: 7, Role: "Viewer"},
			},
		},
	}
	r := mustNewResolver(t, registry(alpha, beta), g, -1)

	got, err := r.Resolve(context.Background(), Caller{Email: "u@example.com"})
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

func TestResolver_Resolve_DropsRoleNoneAndUnknownOrgs(t *testing.T) {
	// Descriptor exists for orgID 42 but not 99. Role "None" also dropped.
	alpha := newDesc("alpha", "Alpha", 42)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 5},
		orgs: map[int64][]Membership{
			5: {
				{OrgID: 42, Role: "None"},  // dropped: no role
				{OrgID: 99, Role: "Admin"}, // dropped: no matching descriptor
			},
		},
	}
	r := mustNewResolver(t, registry(alpha), g, -1)

	got, _ := r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	if len(got) != 0 {
		t.Errorf("expected empty access, got %v", got)
	}
}

func TestResolver_Resolve_UserNeverLoggedIn(t *testing.T) {
	// Grafana returns 404 on lookup → found=false → resolver returns empty
	// without erroring, so the UX is "no access yet; log into Grafana first".
	g := &fakeGrafana{users: map[string]int64{} /* empty */}
	r := mustNewResolver(t, registry(), g, -1)

	got, err := r.Resolve(context.Background(), Caller{Email: "new@e.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestResolver_Resolve_Cache(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, 100)

	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	if g.calls.lookup != 1 || g.calls.userOrgs != 1 {
		t.Errorf("expected 1 lookup + 1 userOrgs call, got %d/%d", g.calls.lookup, g.calls.userOrgs)
	}
}

func TestResolver_Require_InsufficientRole(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, -1)

	_, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleAdmin)
	if err == nil {
		t.Fatalf("expected insufficient-role error, got nil")
	}
}

func TestResolver_Require_NotAuthorised(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {}}, // user exists but in no orgs
	}
	r := mustNewResolver(t, registry(alpha), g, -1)

	_, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleViewer)
	if err == nil {
		t.Fatalf("expected not-authorised error, got nil")
	}
}

func TestResolver_Require_LookupByDisplayNameCaseInsensitive(t *testing.T) {
	alpha := newDesc("alpha", "Alpha Team", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, -1)

	oa, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "ALPHA TEAM", RoleAdmin)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if oa.Name != "alpha" {
		t.Fatalf("want name=alpha, got %q", oa.Name)
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

func TestOrgAccess_FindDatasourceID(t *testing.T) {
	oa := OrgAccess{
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
		gotID, gotOK := oa.FindDatasourceID(c.need...)
		if gotID != c.wantID || gotOK != c.wantOK {
			t.Errorf("FindDatasourceID(%v) = (%d,%v), want (%d,%v)", c.need, gotID, gotOK, c.wantID, c.wantOK)
		}
	}
}

func TestOrgAccess_HasTenantType(t *testing.T) {
	oa := OrgAccess{
		Tenants: []Tenant{
			{Name: "t1", Types: []TenantType{TenantTypeData}},
			{Name: "t2", Types: []TenantType{TenantTypeAlerting}},
		},
	}
	if !oa.HasTenantType(TenantTypeData) {
		t.Error("HasTenantType(data) = false")
	}
	if !oa.HasTenantType(TenantTypeAlerting) {
		t.Error("HasTenantType(alerting) = false")
	}
	if oa.HasTenantType("nonexistent") {
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

// blockingGrafana lets tests gate LookupUserID on a channel so concurrent
// callers are guaranteed to arrive at the cache miss together. Counts both
// LookupUserID and UserOrgs calls atomically under -race.
type blockingGrafana struct {
	users       map[string]int64
	orgs        map[int64][]Membership
	lookupCalls atomic.Int32
	userOrgs    atomic.Int32
	release     chan struct{}
}

func (b *blockingGrafana) LookupUserID(_ context.Context, loginOrEmail string) (int64, bool, error) {
	b.lookupCalls.Add(1)
	if b.release != nil {
		<-b.release
	}
	id, ok := b.users[loginOrEmail]
	return id, ok, nil
}

func (b *blockingGrafana) UserOrgs(_ context.Context, userID int64) ([]Membership, error) {
	b.userOrgs.Add(1)
	return b.orgs[userID], nil
}

// TestResolver_Singleflight_CollapsesConcurrentCallers proves that N
// concurrent callers on the same cold cache key do exactly ONE upstream
// round-trip, not N. Guards against the "stampede / thundering herd"
// failure mode where a cache expiry under load fans out.
func TestResolver_Singleflight_CollapsesConcurrentCallers(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &blockingGrafana{
		users:   map[string]int64{"u@e.com": 1},
		orgs:    map[int64][]Membership{1: {{OrgID: 1, Role: "Admin"}}},
		release: make(chan struct{}),
	}
	r := mustNewResolver(t, registry(alpha), g, 100)

	const callers = 50
	var started sync.WaitGroup
	started.Add(callers)
	var done sync.WaitGroup
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			started.Done()
			_, err := r.Resolve(context.Background(), Caller{Email: "u@e.com", Subject: "sub-1"})
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

// TestResolver_CacheKeyIsSubjectNotEmail proves the same Subject under two
// different Email values shares a cache entry. Fixes the spoofability
// concern: email can change or be unverified, subject cannot.
func TestResolver_CacheKeyIsSubjectNotEmail(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@old.com": 1, "u@new.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, 100)

	// Same subject, different emails — second call should hit cache.
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@old.com", Subject: "sub-1"})
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@new.com", Subject: "sub-1"})
	if g.calls.lookup != 1 {
		t.Errorf("LookupUserID calls = %d, want 1 (cache keyed on Subject, email change ignored)", g.calls.lookup)
	}
}

// TestResolver_ReturnedSlicesAreCloned proves handler mutations of the
// returned OrgAccess don't escape into the cache. Before the fix, appending
// to oa.Datasources silently corrupted every future cache hit.
func TestResolver_ReturnedSlicesAreCloned(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, 100)

	oa1, err := r.Require(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	// Mutation simulating a handler that appends to the datasource list.
	oa1.Datasources = append(oa1.Datasources, Datasource{ID: 999, Name: "poisoned"})

	oa2, err := r.Require(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("second Require: %v", err)
	}
	for _, ds := range oa2.Datasources {
		if ds.ID == 999 {
			t.Fatalf("cache was poisoned by handler-side append (ds=%+v)", ds)
		}
	}
}

// TestResolver_ReturnedTenantTypesAreCloned proves nested-slice mutations
// (oa.Tenants[i].Types) don't escape into the cache either. cloneTenants
// must deep-copy each Tenant's Types slice, not just slices.Clone the outer.
func TestResolver_ReturnedTenantTypesAreCloned(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1, TenantTypeData)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := mustNewResolver(t, registry(alpha), g, 100)

	oa1, err := r.Require(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	oa1.Tenants[0].Types = append(oa1.Tenants[0].Types, TenantTypeAlerting)

	oa2, err := r.Require(context.Background(), Caller{Email: "u@e.com", Subject: "s"}, "alpha", RoleViewer)
	if err != nil {
		t.Fatalf("second Require: %v", err)
	}
	if oa2.HasTenantType(TenantTypeAlerting) {
		t.Fatal("nested Types slice was shared across cache reads")
	}
}

// TestResolver_Require_OrgNotFoundVsNotAuthorised proves that Require
// returns a distinct ErrOrgNotFound when the org simply doesn't exist,
// vs ErrNotAuthorised when the org exists but the caller isn't a member.
// Today both cases are indistinguishable from the caller's side.
func TestResolver_Require_OrgNotFoundVsNotAuthorised(t *testing.T) {
	alpha := newDesc("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {}}, // user exists in Grafana but in no orgs
	}
	r := mustNewResolver(t, registry(alpha), g, -1)

	// alpha exists but the caller isn't a member → ErrNotAuthorised.
	_, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleViewer)
	if err == nil {
		t.Fatal("expected error for known-org-but-no-access, got nil")
	}
	if errors.Is(err, ErrOrgNotFound) {
		t.Errorf("err = %v, want NOT-ErrOrgNotFound (alpha is in the registry)", err)
	}

	// nonexistent has no descriptor → ErrOrgNotFound.
	_, err = r.Require(context.Background(), Caller{Email: "u@e.com"}, "nonexistent", RoleViewer)
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("err = %v, want wraps ErrOrgNotFound", err)
	}
}

// TestResolver_Role_AtLeast guards the iota ordering — future reorders
// that would silently break privilege checks fail here.
func TestResolver_Role_AtLeast(t *testing.T) {
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
