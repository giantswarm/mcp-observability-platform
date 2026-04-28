package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// fakeGrafanaServer satisfies the few endpoints upstream's GrafanaClient
// pings during construction (frontend settings + a minimal health). Without
// this the registrar tests do real DNS against "http://g" and slow each
// test by several seconds.
func fakeGrafanaServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/frontend/settings", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"appUrl":"http://example/"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// fakeGrafana implements grafana.Client by embedding the interface (so any
// method we don't override panics) plus a stub for the one method the
// registrar calls.
type fakeGrafana struct {
	grafana.Client // embedded — unused methods nil-panic
	uid            string
	uidErr         error
	gotID          int64
	gotOpts        grafana.RequestOpts
}

func (f *fakeGrafana) LookupDatasourceUIDByID(_ context.Context, opts grafana.RequestOpts, id int64) (string, error) {
	f.gotID = id
	f.gotOpts = opts
	if f.uidErr != nil {
		return "", f.uidErr
	}
	return f.uid, nil
}

// callerCtx attaches an OAuth caller to ctx using the same plumbing the
// HTTP boundary uses. authz.CallerSubject(ctx) then returns sub.
func callerCtx(sub, email string) context.Context {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = r.WithContext(oauth.ContextWithUserInfo(r.Context(), &providers.UserInfo{ID: sub, Email: email}))
	return authz.PromoteOAuthCaller(context.Background(), r)
}

// stubTool builds a minimal mcpgrafana.Tool whose handler records what the
// registrar passed in: the GrafanaConfig, the datasourceUid arg (if any),
// and the request name.
func stubTool(name string, required []string, captured *capturedCall) mcpgrafana.Tool {
	t := mcp.NewTool(name, mcp.WithDescription("stub"))
	t.InputSchema.Properties = map[string]any{
		"datasourceUid": map[string]any{"type": "string"},
		"other":         map[string]any{"type": "string"},
	}
	t.InputSchema.Required = slices.Clone(required)
	return mcpgrafana.Tool{
		Tool: t,
		Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			captured.cfg = mcpgrafana.GrafanaConfigFromContext(ctx)
			captured.args = req.GetArguments()
			captured.toolName = req.Params.Name
			return mcp.NewToolResultText("ok"), nil
		},
	}
}

type capturedCall struct {
	cfg      mcpgrafana.GrafanaConfig
	args     map[string]any
	toolName string
}

// orgWithDatasources is a stock authz.Organization fixture.
func orgWithDatasources() authz.Organization {
	return authz.Organization{
		Name:        "acme",
		DisplayName: "Acme",
		OrgID:       7,
		Role:        authz.RoleViewer,
		Datasources: []authz.Datasource{
			{ID: 11, Name: "mimir-acme"},
			{ID: 22, Name: "loki-acme"},
		},
	}
}

// ---------- withOrg ----------

func TestWithOrg_AddsRequiredOrgArg(t *testing.T) {
	in := mcp.NewTool("foo", mcp.WithDescription("d"))
	in.InputSchema.Properties = map[string]any{"x": map[string]any{"type": "number"}}
	in.InputSchema.Required = []string{"x"}

	out := withOrg(in, "")

	if _, ok := out.InputSchema.Properties["org"].(map[string]any); !ok {
		t.Fatal("output missing 'org' in Properties")
	}
	if got := out.InputSchema.Required; len(got) != 2 || got[0] != "org" || got[1] != "x" {
		t.Errorf("Required = %v, want [org x]", got)
	}
	// Input must not have been mutated.
	if _, ok := in.InputSchema.Properties["org"]; ok {
		t.Error("withOrg mutated input.Properties")
	}
	if slices.Contains(in.InputSchema.Required, "org") {
		t.Error("withOrg mutated input.Required")
	}
}

func TestWithOrg_PanicsOnOrgCollision(t *testing.T) {
	in := mcp.NewTool("foo", mcp.WithDescription("d"))
	in.InputSchema.Properties = map[string]any{"org": map[string]any{"type": "string"}}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on org-arg collision, got none")
		}
	}()
	_ = withOrg(in, "")
}

func TestWithOrg_ReplaceArg_RemovesDatasourceUid(t *testing.T) {
	in := mcp.NewTool("foo", mcp.WithDescription("d"))
	in.InputSchema.Properties = map[string]any{
		"datasourceUid": map[string]any{"type": "string"},
		"y":             map[string]any{"type": "number"},
	}
	in.InputSchema.Required = []string{"datasourceUid", "y"}

	out := withOrg(in, DatasourceUIDArg)

	if _, has := out.InputSchema.Properties["datasourceUid"]; has {
		t.Error("output still exposes datasourceUid in Properties; registrar fills it server-side")
	}
	if slices.Contains(out.InputSchema.Required, "datasourceUid") {
		t.Error("output still requires datasourceUid")
	}
	if _, ok := out.InputSchema.Properties["org"]; !ok {
		t.Error("output missing 'org' in Properties")
	}
	if got := out.InputSchema.Required; len(got) != 2 || got[0] != "org" || got[1] != "y" {
		t.Errorf("Required = %v, want [org y]", got)
	}
	// Input must not have been mutated.
	if _, has := in.InputSchema.Properties["org"]; has {
		t.Error("withOrg mutated input.Properties")
	}
	if !slices.Contains(in.InputSchema.Required, "datasourceUid") {
		t.Error("withOrg mutated input.Required")
	}
}

// ---------- NewRegistrar ----------

func TestNewRegistrar_Validation(t *testing.T) {
	az := &authztest.Fake{}
	gc := &fakeGrafana{}
	cases := []struct {
		name      string
		az        authz.Authorizer
		gc        grafana.Client
		url       string
		apiKey    string
		basicAuth *url.Userinfo
		wantErr   string
	}{
		{"happy_apikey", az, gc, "http://g", "tok", nil, ""},
		{"happy_basic", az, gc, "http://g", "", url.UserPassword("u", "p"), ""},
		{"nil_authorizer", nil, gc, "http://g", "tok", nil, "Authorizer"},
		{"nil_grafana", az, nil, "http://g", "tok", nil, "Grafana"},
		{"empty_url", az, gc, "", "tok", nil, "URL"},
		{"both_creds", az, gc, "http://g", "tok", url.UserPassword("u", "p"), "exactly one"},
		{"no_creds", az, gc, "http://g", "", nil, "exactly one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewRegistrar(c.az, c.gc, c.url, c.apiKey, c.basicAuth)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

// ---------- Org / wrap (org-only path) ----------

func TestRegistrar_Wrap_MissingOrg(t *testing.T) {
	ts := fakeGrafanaServer(t)
	r, _ := NewRegistrar(&authztest.Fake{}, &fakeGrafana{}, ts.URL, "tok", nil)
	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, "", "", stubTool("t", nil, captured))

	res, err := h(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result for missing org arg")
	}
	if captured.toolName != "" {
		t.Error("upstream handler should not be called when org arg is missing")
	}
}

func TestRegistrar_Wrap_AuthzDenied(t *testing.T) {
	az := &authztest.Fake{Err: errors.New("not authorised")}
	ts := fakeGrafanaServer(t)
	r, _ := NewRegistrar(az, &fakeGrafana{}, ts.URL, "tok", nil)
	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, "", "", stubTool("t", nil, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result on authz denial")
	}
	if captured.toolName != "" {
		t.Error("upstream handler should not be called on authz denial")
	}
}

func TestRegistrar_Wrap_HappyPath_HeaderPropagation(t *testing.T) {
	az := &authztest.Fake{Org: orgWithDatasources()}
	ts := fakeGrafanaServer(t)
	r, _ := NewRegistrar(az, &fakeGrafana{}, ts.URL, "tok", nil)
	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, "", "", stubTool("t", nil, captured))

	ctx := callerCtx("sub-123", "alice@example.com")
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{"org": "acme"}}}
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError on happy path: %+v", res)
	}
	if az.GotRef != "acme" || az.GotMin != authz.RoleViewer {
		t.Errorf("authz called with (%q, %v), want (acme, Viewer)", az.GotRef, az.GotMin)
	}
	if captured.cfg.OrgID != 7 {
		t.Errorf("OrgID = %d, want 7", captured.cfg.OrgID)
	}
	if got := captured.cfg.ExtraHeaders["X-Grafana-User"]; got != "sub-123" {
		t.Errorf("X-Grafana-User = %q, want sub-123", got)
	}
}

func TestRegistrar_Wrap_SkipsHeaderOnEmptySubject(t *testing.T) {
	az := &authztest.Fake{Org: orgWithDatasources()}
	ts := fakeGrafanaServer(t)
	r, _ := NewRegistrar(az, &fakeGrafana{}, ts.URL, "tok", nil)
	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, "", "", stubTool("t", nil, captured))

	// No caller in ctx — CallerSubject returns "".
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if _, has := captured.cfg.ExtraHeaders["X-Grafana-User"]; has {
		t.Error("X-Grafana-User should be omitted when caller subject is empty")
	}
}

// ---------- Datasource path ----------

func TestRegistrar_Datasource_InjectsUID(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgWithDatasources()}
	gc := &fakeGrafana{uid: "mimir-uid-xyz"}
	r, _ := NewRegistrar(az, gc, ts.URL, "tok", nil)

	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, authz.DSKindMimir, DatasourceUIDArg,
		stubTool("query_prometheus", []string{"datasourceUid"}, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "query_prometheus", Arguments: map[string]any{"org": "acme", "expr": "up"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if got := gc.gotID; got != 11 {
		t.Errorf("looked up datasource id %d, want 11 (mimir-acme)", got)
	}
	if captured.args["datasourceUid"] != "mimir-uid-xyz" {
		t.Errorf("datasourceUid = %v, want mimir-uid-xyz", captured.args["datasourceUid"])
	}
	// Original args preserved.
	if captured.args["expr"] != "up" {
		t.Errorf("expr arg lost: %v", captured.args["expr"])
	}
	// org arg preserved (visible to upstream too — harmless since upstream ignores unknown args).
	if captured.args["org"] != "acme" {
		t.Errorf("org arg lost: %v", captured.args["org"])
	}
}

func TestRegistrar_Datasource_NoMatchingDatasource(t *testing.T) {
	org := orgWithDatasources()
	org.Datasources = []authz.Datasource{{ID: 99, Name: "tempo-only"}} // no mimir
	az := &authztest.Fake{Org: org}
	ts := fakeGrafanaServer(t)
	r, _ := NewRegistrar(az, &fakeGrafana{}, ts.URL, "tok", nil)

	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, authz.DSKindMimir, DatasourceUIDArg, stubTool("t", []string{"datasourceUid"}, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when org has no matching datasource")
	}
	if captured.toolName != "" {
		t.Error("upstream handler should not be called when datasource is missing")
	}
}

func TestRegistrar_Datasource_UIDLookupFails(t *testing.T) {
	az := &authztest.Fake{Org: orgWithDatasources()}
	gc := &fakeGrafana{uidErr: errors.New("grafana down")}
	r, _ := NewRegistrar(az, gc, "http://g", "tok", nil)

	captured := &capturedCall{}
	h := r.wrap(authz.RoleViewer, authz.DSKindMimir, DatasourceUIDArg, stubTool("t", []string{"datasourceUid"}, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on UID lookup failure")
	}
	if captured.toolName != "" {
		t.Error("upstream handler should not be called when UID lookup fails")
	}
}

// ---------- injectArg ----------

func TestInjectArg_NilArguments(t *testing.T) {
	req := &mcp.CallToolRequest{}
	injectArg(req, "k", "v")
	got, ok := req.Params.Arguments.(map[string]any)
	if !ok || got["k"] != "v" {
		t.Fatalf("Arguments = %#v, want map[k:v]", req.Params.Arguments)
	}
}

func TestInjectArg_PreservesOriginalMap(t *testing.T) {
	original := map[string]any{"a": 1}
	req := &mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: original}}
	injectArg(req, "k", "v")

	// Caller's map MUST NOT be mutated — the request shape is shared with
	// the caller through Params.Arguments-by-reference.
	if _, has := original["k"]; has {
		t.Error("injectArg mutated the caller's map; should copy-on-write")
	}
	got := req.Params.Arguments.(map[string]any)
	if got["a"] != 1 || got["k"] != "v" {
		t.Errorf("Arguments = %#v, want a=1 + k=v", got)
	}
}
