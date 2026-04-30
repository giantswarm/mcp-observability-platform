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

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
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

// oauthCtx attaches a caller identity to ctx; CallerSubject(ctx) returns sub.
func oauthCtx(sub, email string) context.Context {
	return authz.WithCaller(context.Background(), authz.Caller{Subject: sub, Email: email})
}

// stubTool builds a minimal mcpgrafana.Tool whose handler records what the
// binder passed in: the GrafanaConfig, the datasourceUid arg (if any),
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

// orgFixture is a stock authz.Organization. Tenants carry both data and
// alerting types so the binder's tenant-type gate passes regardless of
// which kind a test exercises. Datasources are no longer part of the
// authz domain entity — fixtures configure them on the fakeGrafana via
// listDS.
func orgFixture() authz.Organization {
	return authz.Organization{
		Name:        "acme",
		DisplayName: "Acme",
		OrgID:       7,
		Role:        authz.RoleViewer,
		Tenants: []authz.Tenant{
			{Name: "acme", Types: []authz.TenantType{authz.TenantTypeData, authz.TenantTypeAlerting}},
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

func TestWithOrg_DemoteArg_KeepsArgWithHint(t *testing.T) {
	in := mcp.NewTool("foo", mcp.WithDescription("d"))
	in.InputSchema.Properties = map[string]any{
		"datasourceUid": map[string]any{"type": "string", "description": "Upstream desc."},
		"y":             map[string]any{"type": "number"},
	}
	in.InputSchema.Required = []string{"datasourceUid", "y"}

	out := withOrg(in, datasourceUIDArg)

	prop, ok := out.InputSchema.Properties["datasourceUid"].(map[string]any)
	if !ok {
		t.Fatal("output dropped datasourceUid; demote should keep it visible")
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "Upstream desc.") || !strings.Contains(desc, "list_datasources") {
		t.Errorf("description = %q, want upstream prefix + datasourceUIDHint suffix", desc)
	}
	if slices.Contains(out.InputSchema.Required, "datasourceUid") {
		t.Error("output still requires datasourceUid; demote moves it out of Required")
	}
	if got := out.InputSchema.Required; len(got) != 2 || got[0] != "org" || got[1] != "y" {
		t.Errorf("Required = %v, want [org y]", got)
	}
	// Input must not have been mutated.
	if _, has := in.InputSchema.Properties["org"]; has {
		t.Error("withOrg mutated input.Properties")
	}
	if origDesc, _ := in.InputSchema.Properties["datasourceUid"].(map[string]any)["description"].(string); origDesc != "Upstream desc." {
		t.Errorf("withOrg mutated input description: %q", origDesc)
	}
	if !slices.Contains(in.InputSchema.Required, "datasourceUid") {
		t.Error("withOrg mutated input.Required")
	}
}

// Regression: upstream mcp-grafana tools are built with MustTool, which
// stores the schema as RawInputSchema. mcp.Tool.MarshalJSON ignores the
// structured InputSchema in that case, so the synthetic "org" arg never
// reached the wire and clients couldn't pass it. withOrg must normalize
// the raw schema and emit a structured one whose marshaled JSON includes
// "org" under properties and required.
func TestWithOrg_HandlesRawInputSchema(t *testing.T) {
	in := mcp.NewTool("foo", mcp.WithDescription("d"))
	in.InputSchema = mcp.ToolInputSchema{}
	in.RawInputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"datasourceUid":{"type":"string","description":"Upstream desc."},"y":{"type":"number"}},
		"required":["datasourceUid","y"]
	}`)

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
	if _, ok := marshaled.InputSchema.Properties["org"].(map[string]any); !ok {
		t.Errorf("marshaled schema missing 'org' under properties: %s", blob)
	}
	if !slices.Contains(marshaled.InputSchema.Required, "org") {
		t.Errorf("marshaled schema missing 'org' in required: %v", marshaled.InputSchema.Required)
	}
	if slices.Contains(marshaled.InputSchema.Required, "datasourceUid") {
		t.Errorf("demote should drop datasourceUid from required: %v", marshaled.InputSchema.Required)
	}
	dsProp, _ := marshaled.InputSchema.Properties["datasourceUid"].(map[string]any)
	desc, _ := dsProp["description"].(string)
	if !strings.Contains(desc, "Upstream desc.") || !strings.Contains(desc, "list_datasources") {
		t.Errorf("description = %q, want upstream prefix + datasourceUIDHint suffix", desc)
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
		{"happy_apikey", az, gc, "http://g", "tok", nil, ""},
		{"happy_basic", az, gc, "http://g", "", url.UserPassword("u", "p"), ""},
		{"nil_authorizer", nil, gc, "http://g", "tok", nil, "authorizer"},
		{"nil_grafana", az, nil, "http://g", "tok", nil, "grafana"},
		{"empty_url", az, gc, "", "tok", nil, "URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := newGFBinder(c.az, c.gc, c.url, c.apiKey, c.basicAuth, nil)
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
	b, _ := newGFBinder(&authztest.Fake{}, &fakeGrafana{}, ts.URL, "tok", nil, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

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

func TestBinder_Wrap_AuthzDenied(t *testing.T) {
	az := &authztest.Fake{Err: errors.New("not authorised")}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, "tok", nil, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

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

func TestBinder_Wrap_HappyPath_HeaderPropagation(t *testing.T) {
	az := &authztest.Fake{Org: orgFixture()}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, "tok", nil, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

	ctx := oauthCtx("sub-123", "alice@example.com")
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

func TestBinder_Wrap_SkipsHeaderOnEmptySubject(t *testing.T) {
	az := &authztest.Fake{Org: orgFixture()}
	ts := fakeGrafanaServer(t)
	b, _ := newGFBinder(az, &fakeGrafana{}, ts.URL, "tok", nil, nil)
	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, "", "", "", stubTool("t", nil, captured))

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

// threeMimirsAndOneLoki mirrors the graveler/giantswarm shape: a
// multi-tenant aggregate, two mono-tenant rulers, plus a Loki for
// type-mismatch tests. Order is the order Grafana would return.
func threeMimirsAndOneLoki() []grafana.Datasource {
	return []grafana.Datasource{
		{ID: 2, UID: "u-mimir", Name: "GS Mimir", Type: "prometheus", ManageAlerts: false},
		{ID: 18, UID: "u-mimir-gs", Name: "GS Mimir (giantswarm)", Type: "prometheus", ManageAlerts: true},
		{ID: 21, UID: "u-mimir-ne", Name: "GS Mimir (notempty)", Type: "prometheus", ManageAlerts: true},
		{ID: 30, UID: "u-loki", Name: "GS Loki", Type: "loki", ManageAlerts: true},
	}
}

// Default behaviour: no datasourceUid arg → first matching DS by type
// is picked (the multi-tenant aggregate on graveler).
func TestBinder_Single_DefaultPicksFirstMatch(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listDS: threeMimirsAndOneLoki()}
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", []string{"datasourceUid"}, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "query_prometheus", Arguments: map[string]any{"org": "acme", "expr": "up"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if got := captured.args["datasourceUid"]; got != "u-mimir" {
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", nil, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "query_prometheus", Arguments: map[string]any{"org": "acme", "expr": "up", "datasourceUid": "u-mimir-gs"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res)
	}
	if got := captured.args["datasourceUid"]; got != "u-mimir-gs" {
		t.Errorf("upstream got datasourceUid = %v, want u-mimir-gs", got)
	}
	if gc.gotLookup != "u-mimir-gs" {
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", nil, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "query_prometheus", Arguments: map[string]any{"org": "acme", "datasourceUid": "u-loki"}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on type mismatch")
	}
	if !strings.Contains(textOf(res), "loki") {
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", nil, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "query_prometheus", Arguments: map[string]any{"org": "acme", "datasourceUid": "forged-uid"}}}
	res, err := h(context.Background(), req)
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", nil, captured))

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

// ListDatasources error → tool error, upstream not called.
func TestBinder_Single_ListDatasourcesError(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{listErr: errors.New("grafana down")}
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)

	captured := &capturedCall{}
	h := b.wrap(authz.RoleViewer, authz.TenantTypeData, grafana.DSTypePrometheus, datasourceUIDArg,
		stubTool("query_prometheus", nil, captured))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
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
	respond func(args map[string]any) (*mcp.CallToolResult, error)
}

func stubFanoutTool(name string, cap *fanoutStub) mcpgrafana.Tool {
	t := mcp.NewTool(name, mcp.WithDescription("stub"))
	t.InputSchema.Properties = map[string]any{
		datasourceUIDArgSnake: map[string]any{"type": "string"},
	}
	return mcpgrafana.Tool{
		Tool: t,
		Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cap.calls = append(cap.calls, fanoutCall{
				args: req.GetArguments(),
				cfg:  mcpgrafana.GrafanaConfigFromContext(ctx),
			})
			if cap.respond != nil {
				return cap.respond(req.GetArguments())
			}
			return mcp.NewToolResultText(`[]`), nil
		},
	}
}

func TestBinder_Fanout_FiltersAndIteratesRulerDatasources(t *testing.T) {
	ts := fakeGrafanaServer(t)
	az := &authztest.Fake{Org: orgFixture()}
	gc := &fakeGrafana{
		listDS: []grafana.Datasource{
			{ID: 1, UID: "u1", Name: "mimir-mt", Type: "prometheus", ManageAlerts: false},
			{ID: 2, UID: "u2", Name: "mimir-gs", Type: "prometheus", ManageAlerts: true},
			{ID: 3, UID: "u3", Name: "loki-gs", Type: "loki", ManageAlerts: true},
			{ID: 4, UID: "u4", Name: "tempo-gs", Type: "tempo", ManageAlerts: true},
		},
	}
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)
	cap := &fanoutStub{
		respond: func(args map[string]any) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(fmt.Sprintf(`[{"uid":%q}]`, args[datasourceUIDArgSnake])), nil
		},
	}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool("alerting_manage_rules", cap))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "alerting_manage_rules", Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)
	cap := &fanoutStub{}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool("alerting_manage_rules", cap))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "alerting_manage_rules", Arguments: map[string]any{"org": "acme", datasourceUIDArgSnake: "pinned"}}}
	res, err := h(context.Background(), req)
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
			{ID: 1, UID: "u1", Name: "good", Type: "prometheus", ManageAlerts: true},
			{ID: 2, UID: "u2", Name: "bad", Type: "prometheus", ManageAlerts: true},
		},
	}
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)
	cap := &fanoutStub{
		respond: func(args map[string]any) (*mcp.CallToolResult, error) {
			if args[datasourceUIDArgSnake] == "u2" {
				return mcp.NewToolResultError("400 no valid org id found"), nil
			}
			return mcp.NewToolResultText(`[{"name":"r"}]`), nil
		},
	}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool("alerting_manage_rules", cap))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "alerting_manage_rules", Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
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
	b, _ := newGFBinder(az, gc, ts.URL, "tok", nil, nil)
	cap := &fanoutStub{}
	h := b.wrapFanout(authz.RoleViewer, authz.TenantTypeData, datasourceUIDArgSnake, stubFanoutTool("alerting_manage_rules", cap))

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "alerting_manage_rules", Arguments: map[string]any{"org": "acme"}}}
	res, err := h(context.Background(), req)
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
	req := &mcp.CallToolRequest{}
	if err := injectArg(req, "k", "v"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := req.Params.Arguments.(map[string]any)
	if !ok || got["k"] != "v" {
		t.Fatalf("Arguments = %#v, want map[k:v]", req.Params.Arguments)
	}
}

func TestInjectArg_PreservesOriginalMap(t *testing.T) {
	original := map[string]any{"a": 1}
	req := &mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: original}}
	if err := injectArg(req, "k", "v"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

func TestInjectArg_RejectsMalformedRawMessage(t *testing.T) {
	req := &mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: json.RawMessage("not-json")}}
	if err := injectArg(req, "k", "v"); err == nil {
		t.Fatal("expected error decoding malformed json.RawMessage, got nil")
	}
}

func TestInjectArg_RejectsUnknownShape(t *testing.T) {
	req := &mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: 42}}
	if err := injectArg(req, "k", "v"); err == nil {
		t.Fatal("expected error on unknown Arguments type, got nil")
	}
}
