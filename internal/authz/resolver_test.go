package authz

import (
	"context"
	"testing"
	"time"

	obsv1alpha2 "github.com/giantswarm/observability-operator/api/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := obsv1alpha2.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// newOrg builds a GrafanaOrganization CR fixture. orgID and tenant types
// populate the status so toOrgAccess returns meaningful datasources/tenants.
func newOrg(name, display string, orgID int64, tenantTypes ...obsv1alpha2.TenantType) *obsv1alpha2.GrafanaOrganization {
	tenants := []obsv1alpha2.TenantConfig{}
	if len(tenantTypes) > 0 {
		tenants = []obsv1alpha2.TenantConfig{{Name: obsv1alpha2.TenantID(name), Types: tenantTypes}}
	}
	return &obsv1alpha2.GrafanaOrganization{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: obsv1alpha2.GrafanaOrganizationSpec{
			DisplayName: display,
			RBAC:        &obsv1alpha2.RBAC{},
			Tenants:     tenants,
		},
		Status: obsv1alpha2.GrafanaOrganizationStatus{
			OrgID: orgID,
			DataSources: []obsv1alpha2.DataSource{
				{ID: 10, Name: "mimir-" + name},
				{ID: 11, Name: "loki-" + name},
			},
		},
	}
}

func newFakeReader(t *testing.T, objs ...ctrlclient.Object) ctrlclient.Reader {
	return fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(objs...).
		Build()
}

// fakeGrafana is a deterministic in-memory stub of the GrafanaOrgLookup
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
	alpha := newOrg("alpha", "Alpha", 42, obsv1alpha2.TenantTypeData)
	beta := newOrg("beta", "Beta", 7, obsv1alpha2.TenantTypeAlerting)
	g := &fakeGrafana{
		users: map[string]int64{"u@example.com": 1},
		orgs: map[int64][]Membership{
			1: {
				{OrgID: 42, Role: "Admin"},
				{OrgID: 7, Role: "Viewer"},
			},
		},
	}
	r := NewResolver(newFakeReader(t, alpha, beta), g, nil, 0)

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
	// CR exists for orgID 42 but not 99. Role "None" should also be dropped.
	alpha := newOrg("alpha", "Alpha", 42)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 5},
		orgs: map[int64][]Membership{
			5: {
				{OrgID: 42, Role: "None"},  // dropped: no role
				{OrgID: 99, Role: "Admin"}, // dropped: no matching CR
			},
		},
	}
	r := NewResolver(newFakeReader(t, alpha), g, nil, 0)

	got, _ := r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	if len(got) != 0 {
		t.Errorf("expected empty access, got %v", got)
	}
}

func TestResolver_Resolve_UserNeverLoggedIn(t *testing.T) {
	// Grafana returns 404 on lookup → found=false → resolver returns empty
	// without erroring, so the UX is "no access yet; log into Grafana first".
	g := &fakeGrafana{users: map[string]int64{} /* empty */}
	r := NewResolver(newFakeReader(t), g, nil, 0)

	got, err := r.Resolve(context.Background(), Caller{Email: "new@e.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestResolver_Resolve_Cache(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := NewResolver(newFakeReader(t, alpha), g, nil, time.Minute)

	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	_, _ = r.Resolve(context.Background(), Caller{Email: "u@e.com"})
	if g.calls.lookup != 1 || g.calls.userOrgs != 1 {
		t.Errorf("expected 1 lookup + 1 userOrgs call, got %d/%d", g.calls.lookup, g.calls.userOrgs)
	}
}

func TestResolver_Require_InsufficientRole(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Viewer"}}},
	}
	r := NewResolver(newFakeReader(t, alpha), g, nil, 0)

	_, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleAdmin)
	if err == nil {
		t.Fatalf("expected insufficient-role error, got nil")
	}
}

func TestResolver_Require_NotAuthorised(t *testing.T) {
	alpha := newOrg("alpha", "Alpha", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {}}, // user exists but in no orgs
	}
	r := NewResolver(newFakeReader(t, alpha), g, nil, 0)

	_, err := r.Require(context.Background(), Caller{Email: "u@e.com"}, "alpha", RoleViewer)
	if err == nil {
		t.Fatalf("expected not-authorised error, got nil")
	}
}

func TestResolver_Require_LookupByDisplayNameCaseInsensitive(t *testing.T) {
	alpha := newOrg("alpha", "Alpha Team", 1)
	g := &fakeGrafana{
		users: map[string]int64{"u@e.com": 1},
		orgs:  map[int64][]Membership{1: {{OrgID: 1, Role: "Admin"}}},
	}
	r := NewResolver(newFakeReader(t, alpha), g, nil, 0)

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
		Datasources: []obsv1alpha2.DataSource{
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
		Tenants: []obsv1alpha2.TenantConfig{
			{Name: "t1", Types: []obsv1alpha2.TenantType{obsv1alpha2.TenantTypeData}},
			{Name: "t2", Types: []obsv1alpha2.TenantType{obsv1alpha2.TenantTypeAlerting}},
		},
	}
	if !oa.HasTenantType(obsv1alpha2.TenantTypeData) {
		t.Error("HasTenantType(data) = false")
	}
	if !oa.HasTenantType(obsv1alpha2.TenantTypeAlerting) {
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

func TestCaller_IdentityAndEmpty(t *testing.T) {
	if !(Caller{}).Empty() {
		t.Error("zero Caller should be Empty")
	}
	if (Caller{Subject: "x"}).Empty() {
		t.Error("with Subject should not be Empty")
	}
	if got := (Caller{Email: "e", Login: "l", Subject: "s"}).Identity(); got != "e" {
		t.Errorf("Identity email-preferred = %q, want e", got)
	}
	if got := (Caller{Login: "l", Subject: "s"}).Identity(); got != "l" {
		t.Errorf("Identity login-fallback = %q, want l", got)
	}
	if got := (Caller{Subject: "s"}).Identity(); got != "s" {
		t.Errorf("Identity subject-fallback = %q, want s", got)
	}
}
