package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	mcpgrafana "github.com/grafana/mcp-grafana/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const (
	testOrgName        = "acme"
	testOrgDisplayName = "Acme"
	testOrgArg         = "org"
	testGrafanaURL     = "http://g"
	testDSPrometheus   = "prometheus"
	testDSLoki         = "loki"
	testDSUIDMimirGS   = "u-mimir-gs"
	testToolQueryProm  = "query_prometheus"
	testToolAlertRules = "alerting_rules_read"
	testUID            = "abc"
	testAPIKey         = "tok"
	testArgOther       = "other"
)

// fakeGrafanaServer satisfies the few endpoints upstream's GrafanaClient
// pings during construction (frontend settings + a minimal health). Without
// this the binder tests do real DNS against "http://g" and slow each
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
// method we don't override panics) plus stubs for the methods the binder
// calls. listDS drives both ListDatasources and LookupDatasourceByUID
// so tests need to configure only one source of truth per fixture.
type fakeGrafana struct {
	grafana.Client // embedded — unused methods nil-panic

	listDS    []grafana.Datasource
	listErr   error
	lookupErr error
	gotList   grafana.RequestOpts
	gotLookup string
}

func (f *fakeGrafana) ListDatasources(_ context.Context, opts grafana.RequestOpts) ([]grafana.Datasource, error) {
	f.gotList = opts
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listDS, nil
}

func (f *fakeGrafana) LookupDatasourceByUID(_ context.Context, _ grafana.RequestOpts, uid string) (grafana.Datasource, error) {
	f.gotLookup = uid
	if f.lookupErr != nil {
		return grafana.Datasource{}, f.lookupErr
	}
	for _, ds := range f.listDS {
		if ds.UID == uid {
			return ds, nil
		}
	}
	return grafana.Datasource{}, fmt.Errorf("datasource %q not found in org", uid)
}

// oauthCtx attaches the validated user to the test context.
func oauthCtx(t *testing.T, sub, email string) context.Context {
	return handler.ContextWithUserInfo(t.Context(), &providers.UserInfo{Issuer: "https://issuer.example", ID: sub, Email: email, EmailVerified: true})
}

// stubTool builds a minimal mcpgrafana.Tool whose handler records what the
// binder passed in: the GrafanaConfig, the datasourceUid arg (if any),
// and the request name.
func stubTool(name string, required []string, captured *capturedCall) mcpgrafana.Tool {
	return mcpgrafana.Tool{
		Tool: stubSDKTool(name, map[string]any{
			datasourceUIDArg: map[string]any{jsonSchemaTypeKey: jsonTypeString},
			testArgOther:     map[string]any{jsonSchemaTypeKey: jsonTypeString},
		}, required),
		Handler: func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			captured.cfg = mcpgrafana.GrafanaConfigFromContext(ctx)
			captured.args = sdkArgs(req)
			captured.toolName = req.Params.Name
			return mcpgrafana.NewToolResultText("ok"), nil
		},
	}
}

// stubSDKTool builds an upstream-shaped tool definition: an SDK tool
// whose schema is raw JSON, as mcpgrafana.MustTool produces.
func stubSDKTool(name string, props map[string]any, required []string) *sdkmcp.Tool {
	schema := map[string]any{jsonSchemaTypeKey: "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = slices.Clone(required)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return &sdkmcp.Tool{Name: name, Description: "stub", InputSchema: json.RawMessage(raw)}
}

// sdkArgs decodes the raw arguments an upstream handler receives.
func sdkArgs(req *sdkmcp.CallToolRequest) map[string]any {
	args := map[string]any{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			panic(err)
		}
	}
	return args
}

type capturedCall struct {
	cfg      mcpgrafana.GrafanaConfig
	args     map[string]any
	toolName string
}

// orgFixture is a stock authz.Organization. Tenants carry both data and
// alerting types so the binder's tenant-type gate passes regardless of
// which kind a test exercises. Datasources are no longer part of the
// authz domain entity — fixtures configure them on the fakeGrafana via
// listDS.
func orgFixture() authz.Organization {
	return authz.Organization{
		Name:        testOrgName,
		DisplayName: testOrgDisplayName,
		OrgID:       7,
		Role:        authz.RoleViewer,
		Tenants: []authz.Tenant{
			{Name: testOrgName, Types: []authz.TenantType{authz.TenantTypeData, authz.TenantTypeAlerting}},
		},
	}
}

// ---------- withOrg ----------

func TestWithOrg_AddsRequiredOrgArg(t *testing.T) {
	in := stubSDKTool("foo", map[string]any{"x": map[string]any{jsonSchemaTypeKey: "number"}}, []string{"x"})

	out := withOrg(in, "")

	if _, ok := testSchema(t, out).Properties[testOrgArg].(map[string]any); !ok {
		t.Fatal("output missing 'org' in Properties")
	}
	if got := testSchema(t, out).Required; len(got) != 2 || got[0] != testOrgArg || got[1] != "x" {
		t.Errorf("Required = %v, want [org x]", got)
	}
	// Input must not have been mutated.
	if _, ok := testSchema(t, in).Properties[testOrgArg]; ok {
		t.Error("withOrg mutated input.Properties")
	}
	if slices.Contains(testSchema(t, in).Required, testOrgArg) {
		t.Error("withOrg mutated input.Required")
	}
}

func TestWithOrg_PanicsOnOrgCollision(t *testing.T) {
	in := stubSDKTool("foo", map[string]any{testOrgArg: map[string]any{jsonSchemaTypeKey: jsonTypeString}}, nil)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on org-arg collision, got none")
		}
	}()
	_ = withOrg(in, "")
}

func TestWithOrg_DemoteArg_KeepsArgWithHint(t *testing.T) {
	in := stubSDKTool("foo", map[string]any{
		datasourceUIDArg: map[string]any{jsonSchemaTypeKey: jsonTypeString, "description": "Upstream desc."},
		"y":              map[string]any{jsonSchemaTypeKey: "number"},
	}, []string{datasourceUIDArg, "y"})

	out := withOrg(in, datasourceUIDArg)

	prop, ok := testSchema(t, out).Properties[datasourceUIDArg].(map[string]any)
	if !ok {
		t.Fatal("output dropped datasourceUid; demote should keep it visible")
	}
	desc, _ := prop["description"].(string)
	if desc != datasourceUIDHint {
		t.Errorf("description = %q, want datasourceUIDHint in place of upstream's", desc)
	}
	if slices.Contains(testSchema(t, out).Required, datasourceUIDArg) {
		t.Error("output still requires datasourceUid; demote moves it out of Required")
	}
	if got := testSchema(t, out).Required; len(got) != 2 || got[0] != testOrgArg || got[1] != "y" {
		t.Errorf("Required = %v, want [org y]", got)
	}
	// Input must not have been mutated.
	if _, has := testSchema(t, in).Properties[testOrgArg]; has {
		t.Error("withOrg mutated input.Properties")
	}
	if origDesc, _ := testSchema(t, in).Properties[datasourceUIDArg].(map[string]any)["description"].(string); origDesc != "Upstream desc." {
		t.Errorf("withOrg mutated input description: %q", origDesc)
	}
	if !slices.Contains(testSchema(t, in).Required, datasourceUIDArg) {
		t.Error("withOrg mutated input.Required")
	}
}

func TestWithOrg_HandlesRawInputSchema(t *testing.T) {
	in := &sdkmcp.Tool{Name: "foo", Description: "d", InputSchema: json.RawMessage(`{
		"type":"object",
		"properties":{"datasourceUid":{"type":"string","description":"Upstream desc."},"y":{"type":"number"}},
		"required":["datasourceUid","y"]
	}`)}

	out := withOrg(in, datasourceUIDArg)

	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal output tool: %v", err)
	}
	var marshaled struct {
		InputSchema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(blob, &marshaled); err != nil {
		t.Fatalf("unmarshal marshaled tool: %v", err)
	}
	if _, ok := marshaled.InputSchema.Properties[testOrgArg].(map[string]any); !ok {
		t.Errorf("marshaled schema missing 'org' under properties: %s", blob)
	}
	if !slices.Contains(marshaled.InputSchema.Required, testOrgArg) {
		t.Errorf("marshaled schema missing 'org' in required: %v", marshaled.InputSchema.Required)
	}
	if slices.Contains(marshaled.InputSchema.Required, datasourceUIDArg) {
		t.Errorf("demote should drop datasourceUid from required: %v", marshaled.InputSchema.Required)
	}
	dsProp, _ := marshaled.InputSchema.Properties[datasourceUIDArg].(map[string]any)
	desc, _ := dsProp["description"].(string)
	if desc != datasourceUIDHint {
		t.Errorf("description = %q, want datasourceUIDHint in place of upstream's", desc)
	}
}

// ---------- newGFBinder ----------

func TestNewGFBinder_Validation(t *testing.T) {
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
		{"happy_apikey", az, gc, testGrafanaURL, testAPIKey, nil, ""},
		{"happy_basic", az, gc, testGrafanaURL, "", url.UserPassword("u", "p"), ""},
		{"nil_authorizer", nil, gc, testGrafanaURL, testAPIKey, nil, "authorizer"},
		{"nil_grafana", az, nil, testGrafanaURL, testAPIKey, nil, "grafana"},
		{"empty_url", az, gc, "", testAPIKey, nil, "URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := newGFBinder(c.az, c.gc, c.url, GrafanaAuth{APIKey: c.apiKey, BasicAuth: c.basicAuth}, nil)
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

// ---------- bindOrgTool / wrap (org-only path) ----------

func TestBinder_Wrap_MissingOrg(t *testing.T) {
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(&authztest.Fake{}, &fakeGrafana{}, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	res, err := h(t.Context(), testRequest("", nil))
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

func TestBinder_Wrap_AuthzDenied(t *testing.T) {
	az := &authztest.Fake{Err: errors.New("not authorised")}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	req := testRequest("", map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
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

func TestBinder_Wrap_HappyPath_HeaderPropagation(t *testing.T) {
	az := &authztest.Fake{Org: orgFixture()}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	ctx := oauthCtx(t, "sub-123", "alice@example.com")
	req := testRequest("t", map[string]any{testOrgArg: testOrgName})
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError on happy path: %+v", res)
	}
	if az.GotRef != testOrgName || az.GotMin != authz.RoleViewer {
		t.Errorf("authz called with (%q, %v), want (acme, Viewer)", az.GotRef, az.GotMin)
	}
	if captured.cfg.OrgID != 7 {
		t.Errorf("OrgID = %d, want 7", captured.cfg.OrgID)
	}
	if got := captured.cfg.ExtraHeaders["X-Grafana-User"]; got != "alice@example.com" {
		t.Errorf("X-Grafana-User = %q, want alice@example.com", got)
	}
}

func TestBinder_Wrap_JWT_ForwardsCallerToken(t *testing.T) {
	const header = "X-JWT-Assertion"
	az := &authztest.Fake{Org: orgFixture()}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, GrafanaAuth{JWTHeader: header}, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	ctx := grafana.WithUserToken(oauthCtx(t, "sub-123", "alice@example.com"), "id-token-alice")
	req := testRequest("t", map[string]any{testOrgArg: testOrgName})
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if captured.cfg.APIKey != "" || captured.cfg.BasicAuth != nil {
		t.Errorf("shared credential set in JWT mode: APIKey=%q BasicAuth=%v", captured.cfg.APIKey, captured.cfg.BasicAuth)
	}
	if got := captured.cfg.ExtraHeaders[header]; got != "id-token-alice" {
		t.Errorf("%s = %q, want id-token-alice", header, got)
	}
	if got := captured.cfg.ExtraHeaders["X-Grafana-User"]; got != "alice@example.com" {
		t.Errorf("X-Grafana-User = %q, want alice@example.com", got)
	}
}

func TestBinder_Wrap_SkipsHeaderOnEmptySubject(t *testing.T) {
	az := &authztest.Fake{Org: orgFixture()}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	// Without a validated user, the Grafana audit handle is empty.
	req := testRequest("", map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
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

// threeMimirsAndOneLoki mirrors the graveler/giantswarm shape: a
// multi-tenant aggregate, two mono-tenant rulers, plus a Loki for
// type-mismatch tests. Order is the order Grafana would return.
func threeMimirsAndOneLoki() []grafana.Datasource {
	return []grafana.Datasource{
		{ID: 2, UID: "u-mimir", Name: "GS Mimir", Type: testDSPrometheus, ManageAlerts: false},
		{ID: 18, UID: testDSUIDMimirGS, Name: "GS Mimir (giantswarm)", Type: testDSPrometheus, ManageAlerts: true},
		{ID: 21, UID: "u-mimir-ne", Name: "GS Mimir (notempty)", Type: testDSPrometheus, ManageAlerts: true},
		{ID: 30, UID: "u-loki", Name: "GS Loki", Type: testDSLoki, ManageAlerts: true},
	}
}

// Default behaviour: no datasourceUid arg → first matching DS by type
// is picked (the multi-tenant aggregate on graveler).
func TestBinder_Single_DefaultPicksFirstMatch(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: threeMimirsAndOneLoki()}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, []string{datasourceUIDArg}, captured))

	req := testRequest(testToolQueryProm, map[string]any{testOrgArg: testOrgName, "expr": "up"})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if got := captured.args[datasourceUIDArg]; got != "u-mimir" {
		t.Errorf("datasourceUid = %v, want u-mimir (first prometheus match)", got)
	}
	if captured.args["expr"] != "up" {
		t.Errorf("expr arg lost: %v", captured.args["expr"])
	}
	if gc.gotLookup != "" {
		t.Errorf("LookupDatasourceByUID called with %q on default path; expected ListDatasources only", gc.gotLookup)
	}
}

// Caller-supplied UID overrides the first-match default and is forwarded
// to upstream verbatim, after type validation.
func TestBinder_Single_ExplicitUIDOverrides(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: threeMimirsAndOneLoki()}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, nil, captured))

	req := testRequest(testToolQueryProm, map[string]any{testOrgArg: testOrgName, "expr": "up", datasourceUIDArg: testDSUIDMimirGS})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if got := captured.args[datasourceUIDArg]; got != testDSUIDMimirGS {
		t.Errorf("upstream got datasourceUid = %v, want u-mimir-gs", got)
	}
	if gc.gotLookup != testDSUIDMimirGS {
		t.Errorf("LookupDatasourceByUID called with %q, want u-mimir-gs", gc.gotLookup)
	}
	if gc.gotList.OrgID != 0 {
		t.Errorf("ListDatasources should not be called on explicit-UID path; OrgID=%d", gc.gotList.OrgID)
	}
}

// Caller-supplied UID of the wrong type → error before upstream is hit.
func TestBinder_Single_RejectsUIDFromOtherType(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: threeMimirsAndOneLoki()}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, nil, captured))

	req := testRequest(testToolQueryProm, map[string]any{testOrgArg: testOrgName, datasourceUIDArg: "u-loki"})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on type mismatch")
	}
	if !strings.Contains(textOf(res), testDSLoki) {
		t.Errorf("error should mention actual type; got %q", textOf(res))
	}
	if captured.toolName != "" {
		t.Error("upstream handler must not run on type mismatch")
	}
}

// Caller-supplied UID not in the org's datasource list → error.
func TestBinder_Single_RejectsUIDNotInOrg(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: threeMimirsAndOneLoki()}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, nil, captured))

	req := testRequest(testToolQueryProm, map[string]any{testOrgArg: testOrgName, datasourceUIDArg: "forged-uid"})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on unknown UID")
	}
	if captured.toolName != "" {
		t.Error("upstream handler must not run on unknown UID")
	}
}

// No live datasource of dsType → error before upstream is hit.
func TestBinder_Single_NoMatchingDatasource(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: []grafana.Datasource{
		{ID: 99, UID: "u-tempo", Name: "tempo-only", Type: "tempo"},
	}}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, nil, captured))

	req := testRequest("", map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
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

// ListDatasources error → tool error, upstream not called.
func TestBinder_Single_ListDatasourcesError(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listErr: errors.New("grafana down")}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool(testToolQueryProm, nil, captured))

	req := testRequest("", map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when ListDatasources fails")
	}
	if captured.toolName != "" {
		t.Error("upstream handler must not run when ListDatasources fails")
	}
}

// ---------- bindDatasourceFanoutTool ----------

type fanoutCall struct {
	args map[string]any
	cfg  mcpgrafana.GrafanaConfig
}

type fanoutStub struct {
	calls   []fanoutCall
	respond func(args map[string]any) (*sdkmcp.CallToolResult, error)
}

func stubFanoutTool(name string, cap *fanoutStub) mcpgrafana.Tool {
	return mcpgrafana.Tool{
		Tool: stubSDKTool(name, map[string]any{
			datasourceUIDArgSnake: map[string]any{jsonSchemaTypeKey: jsonTypeString},
		}, nil),
		Handler: func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			args := sdkArgs(req)
			cap.calls = append(cap.calls, fanoutCall{
				args: args,
				cfg:  mcpgrafana.GrafanaConfigFromContext(ctx),
			})
			if cap.respond != nil {
				return cap.respond(args)
			}
			return mcpgrafana.NewToolResultText(`[]`), nil
		},
	}
}

func TestBinder_Fanout_FiltersAndIteratesRulerDatasources(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{
		listDS: []grafana.Datasource{
			{ID: 1, UID: "u1", Name: "mimir-mt", Type: testDSPrometheus, ManageAlerts: false},
			{ID: 2, UID: "u2", Name: "mimir-gs", Type: testDSPrometheus, ManageAlerts: true},
			{ID: 3, UID: "u3", Name: "loki-gs", Type: testDSLoki, ManageAlerts: true},
			{ID: 4, UID: "u4", Name: "tempo-gs", Type: "tempo", ManageAlerts: true},
		},
	}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	cap := &fanoutStub{
		respond: func(args map[string]any) (*sdkmcp.CallToolResult, error) {
			return mcpgrafana.NewToolResultText(fmt.Sprintf(`[{"uid":%q}]`, args[datasourceUIDArgSnake])), nil
		},
	}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool(testToolAlertRules, cap))

	req := testRequest(testToolAlertRules, map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}

	if len(cap.calls) != 2 {
		t.Fatalf("upstream called %d times, want 2 (mimir-gs + loki-gs only)", len(cap.calls))
	}
	gotUIDs := []string{cap.calls[0].args[datasourceUIDArgSnake].(string), cap.calls[1].args[datasourceUIDArgSnake].(string)}
	wantUIDs := []string{"u2", "u3"}
	if !slices.Equal(gotUIDs, wantUIDs) {
		t.Errorf("upstream called for UIDs %v, want %v", gotUIDs, wantUIDs)
	}
	if gc.gotList.OrgID != 7 {
		t.Errorf("ListDatasources OrgID = %d, want 7", gc.gotList.OrgID)
	}

	body := textOf(res)
	var out struct {
		Datasources []struct {
			Name  string          `json:"name"`
			UID   string          `json:"uid"`
			Type  string          `json:"type"`
			Rules json.RawMessage `json:"rules,omitempty"`
			Error string          `json:"error,omitempty"`
		} `json:"datasources"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("merged response not valid JSON: %v\nbody=%s", err, body)
	}
	if len(out.Datasources) != 2 {
		t.Fatalf("merged response has %d entries, want 2: %+v", len(out.Datasources), out)
	}
	for _, e := range out.Datasources {
		if e.Error != "" {
			t.Errorf("entry %s has error: %s", e.UID, e.Error)
		}
		if string(e.Rules) == "" {
			t.Errorf("entry %s missing rules", e.UID)
		}
	}
}

func TestBinder_Fanout_EscapeHatch_BypassesListing(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listErr: errors.New("ListDatasources should not be called")}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	cap := &fanoutStub{}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool(testToolAlertRules, cap))

	req := testRequest(testToolAlertRules, map[string]any{testOrgArg: testOrgName, datasourceUIDArgSnake: "pinned"})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("upstream called %d times, want 1 (escape hatch)", len(cap.calls))
	}
	if cap.calls[0].args[datasourceUIDArgSnake] != "pinned" {
		t.Errorf("escape-hatch UID = %v, want pinned", cap.calls[0].args[datasourceUIDArgSnake])
	}
	if textOf(res) != `[]` {
		t.Errorf("escape-hatch result = %q, want raw upstream passthrough []", textOf(res))
	}
}

func TestBinder_Fanout_PerDatasourceErrorIsTagged(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{
		listDS: []grafana.Datasource{
			{ID: 1, UID: "u1", Name: "good", Type: testDSPrometheus, ManageAlerts: true},
			{ID: 2, UID: "u2", Name: "bad", Type: testDSPrometheus, ManageAlerts: true},
		},
	}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	cap := &fanoutStub{
		respond: func(args map[string]any) (*sdkmcp.CallToolResult, error) {
			if args[datasourceUIDArgSnake] == "u2" {
				return mcpgrafana.NewToolResultError("400 no valid org id found"), nil
			}
			return mcpgrafana.NewToolResultText(`[{"name":"r"}]`), nil
		},
	}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool(testToolAlertRules, cap))

	req := testRequest(testToolAlertRules, map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("merged result must not be IsError; per-DS errors are tagged: %+v", res)
	}

	var out struct {
		Datasources []struct {
			UID   string `json:"uid"`
			Error string `json:"error,omitempty"`
		} `json:"datasources"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Datasources) != 2 {
		t.Fatalf("got %d entries, want 2", len(out.Datasources))
	}
	if out.Datasources[0].UID != "u1" || out.Datasources[0].Error != "" {
		t.Errorf("u1 entry = %+v, want clean", out.Datasources[0])
	}
	if out.Datasources[1].UID != "u2" || !strings.Contains(out.Datasources[1].Error, "400") {
		t.Errorf("u2 entry = %+v, want error containing 400", out.Datasources[1])
	}
}

func TestBinder_Fanout_ListDatasourcesError(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listErr: errors.New("grafana down")}
	b, _ := newGFBinder(az, gc, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	cap := &fanoutStub{}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool(testToolAlertRules, cap))

	req := testRequest(testToolAlertRules, map[string]any{testOrgArg: testOrgName})
	res, err := h(t.Context(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when ListDatasources fails")
	}
	if len(cap.calls) != 0 {
		t.Errorf("upstream called %d times, want 0", len(cap.calls))
	}
}

// ---------- injectArg ----------

func TestInjectArg_NilArguments(t *testing.T) {
	req := testRequest("", nil)
	if err := injectArg(req, "k", "v"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := sdkArgs(req)
	if got["k"] != "v" {
		t.Fatalf("Arguments = %#v, want map[k:v]", req.Params.Arguments)
	}
}

func TestInjectArg_PreservesOriginalMap(t *testing.T) {
	req := testRequest("", map[string]any{"a": 1})
	original := slices.Clone(req.Params.Arguments)
	if err := injectArg(req, "k", "v"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(original) != `{"a":1}` {
		t.Error("injectArg mutated the caller's arguments")
	}
	got := sdkArgs(req)
	if got["a"] != float64(1) || got["k"] != "v" {
		t.Errorf("Arguments = %#v, want a=1 + k=v", got)
	}
}

func TestInjectArg_RejectsMalformedRawMessage(t *testing.T) {
	req := &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage("not-json")}}
	if err := injectArg(req, "k", "v"); err == nil {
		t.Fatal("expected error decoding malformed json.RawMessage, got nil")
	}
}

func TestInjectArg_RejectsUnknownShape(t *testing.T) {
	req := &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage("42")}}
	if err := injectArg(req, "k", "v"); err == nil {
		t.Fatal("expected error on unknown Arguments type, got nil")
	}
}

func TestWithOrg_PreservesNativeAnnotationsAndSchema(t *testing.T) {
	in := stubSDKTool("t", map[string]any{
		datasourceUIDArg: map[string]any{jsonSchemaTypeKey: jsonTypeString},
		testArgOther:     map[string]any{jsonSchemaTypeKey: jsonTypeString},
	}, []string{datasourceUIDArg, testArgOther})
	in.Annotations = &sdkmcp.ToolAnnotations{
		Title:           "T",
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: new(false),
	}

	out := withOrg(in, datasourceUIDArg)

	if out.Name != "t" || out.Description != "stub" {
		t.Errorf("name/description = %q/%q", out.Name, out.Description)
	}
	a := out.Annotations
	if a.Title != "T" || !a.ReadOnlyHint || !a.IdempotentHint || *a.DestructiveHint || a.OpenWorldHint != nil {
		t.Errorf("annotations = %+v", a)
	}
	if !slices.Equal(testSchema(t, out).Required, []string{testOrgArg, testArgOther}) {
		t.Errorf("required = %v, want [org other]", testSchema(t, out).Required)
	}
	for _, p := range []string{testOrgArg, datasourceUIDArg, testArgOther} {
		if _, ok := testSchema(t, out).Properties[p]; !ok {
			t.Errorf("property %q missing", p)
		}
	}
}

func TestInvokeUpstream_PreservesNativeRequestAndResult(t *testing.T) {
	var gotName string
	var gotArgs map[string]any
	upstream := mcpgrafana.Tool{
		Tool: stubSDKTool("t", nil, nil),
		Handler: func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			gotName = req.Params.Name
			gotArgs = sdkArgs(req)
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: "hello"},
					&sdkmcp.ImageContent{Data: []byte("png"), MIMEType: "image/png"},
				},
				IsError: true,
			}, nil
		},
	}
	req := testRequest("t", map[string]any{"k": "v"})

	res, err := invokeUpstream(t.Context(), upstream, req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if gotName != "t" || gotArgs["k"] != "v" {
		t.Errorf("upstream got name=%q args=%v", gotName, gotArgs)
	}
	if !res.IsError {
		t.Error("IsError lost")
	}
	if len(res.Content) != 2 {
		t.Fatalf("got %d content items, want 2", len(res.Content))
	}
	if textOf(res) != "hello" {
		t.Errorf("text = %q, want hello", textOf(res))
	}
	img, ok := res.Content[1].(*sdkmcp.ImageContent)
	if !ok {
		t.Fatalf("content[1] is %T, want mcp.ImageContent", res.Content[1])
	}
	if string(img.Data) != "png" || img.MIMEType != "image/png" {
		t.Errorf("image = %+v", img)
	}
}

func TestCallUpstream_PropagatesHandlerError(t *testing.T) {
	want := errors.New("boom")
	upstream := mcpgrafana.Tool{
		Tool: stubSDKTool("t", nil, nil),
		Handler: func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return nil, want
		},
	}
	_, err := invokeUpstream(t.Context(), upstream, testRequest("", nil))
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestBinderPreservesNativeContinuationAndPrecision(t *testing.T) {
	ts := fakeGrafanaServer(t)
	binder, err := newGFBinder(&authztest.Fake{Org: orgFixture()}, &fakeGrafana{}, ts.URL, GrafanaAuth{APIKey: testAPIKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := testRequest("native", nil)
	original.Params.Arguments = json.RawMessage(`{"org":"acme","large":9007199254740993}`)
	original.Params.RequestState = "opaque-continuation"
	original.Params.InputResponses = sdkmcp.InputResponseMap{"approval": &sdkmcp.ElicitResult{Action: "accept"}}
	original.Params.Meta = sdkmcp.Meta{"progressToken": "progress"}
	original.Extra = &sdkmcp.RequestExtra{Header: http.Header{"Traceparent": []string{"trace"}}}
	expected := &sdkmcp.CallToolResult{StructuredContent: map[string]any{"native": true}, Meta: sdkmcp.Meta{"custom": "value"}}
	upstream := mcpgrafana.Tool{Tool: stubSDKTool("native", nil, nil), Handler: func(_ context.Context, incoming *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		if incoming == original || incoming.Params == original.Params {
			t.Fatal("mutable request reused")
		}
		if incoming.Extra != original.Extra || incoming.Session != original.Session {
			t.Fatal("native transport context lost")
		}
		if incoming.Params.RequestState != original.Params.RequestState || incoming.Params.InputResponses["approval"] != original.Params.InputResponses["approval"] || incoming.Params.Meta["progressToken"] != "progress" {
			t.Fatal("native continuation lost")
		}
		if string(incoming.Params.Arguments) != `{"large":9007199254740993}` {
			t.Fatalf("precision or org filtering changed: %s", incoming.Params.Arguments)
		}
		return expected, nil
	}}
	result, err := binder.wrap(authz.RoleViewer, "", "", "", upstream)(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	if result != expected {
		t.Fatal("native result converted")
	}
	if string(original.Params.Arguments) != `{"org":"acme","large":9007199254740993}` {
		t.Fatal("caller arguments mutated")
	}
}
