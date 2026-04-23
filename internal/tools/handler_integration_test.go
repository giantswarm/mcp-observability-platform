// Handler integration tests for the five hottest tools. Each wires a real
// grafana.Client against an httptest.Server that mocks the relevant Grafana
// endpoints, uses a fake Authorizer, invokes the tool's handler directly
// via MCPServer.GetTool, and asserts on both the downstream HTTP shape and
// the tool result. Catches the orchestration bugs (arg parsing, org
// resolution, response projection) that pure-helper tests miss.
package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// fakeAuthz is a stand-in Authorizer for handler-integration tests. It
// bypasses the Grafana user/org lookup (covered by authz tests) and hands
// every caller the same fully-populated Organization — so tests can focus
// on the tool-handler side of the pipeline.
type fakeAuthz struct{ org authz.Organization }

func (f fakeAuthz) RequireOrg(_ context.Context, _ authz.Caller, _ string, _ authz.Role) (authz.Organization, error) {
	return f.org, nil
}

func (f fakeAuthz) ListOrgs(_ context.Context, _ authz.Caller) (map[string]authz.Organization, error) {
	return map[string]authz.Organization{f.org.Name: f.org}, nil
}

// wireHandlerTest builds the full tool surface against an httptest.Server.
// Returns the MCPServer (use GetTool to retrieve a handler) and a cleanup.
func wireHandlerTest(t *testing.T, ts *httptest.Server) *mcpsrv.MCPServer {
	t.Helper()
	gf, err := grafana.New(grafana.Config{URL: ts.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("grafana.New: %v", err)
	}
	az := fakeAuthz{org: authz.Organization{
		Name:        "acme",
		DisplayName: "Acme",
		OrgID:       1,
		Role:        authz.RoleAdmin,
		Tenants: []authz.Tenant{{
			Name:  "acme",
			Types: []authz.TenantType{authz.TenantTypeData, authz.TenantTypeAlerting},
		}},
		Datasources: []authz.Datasource{
			{ID: 10, Name: "mimir-acme"},
			{ID: 11, Name: "loki-acme"},
			{ID: 12, Name: "tempo-acme"},
			{ID: 13, Name: "alertmanager-acme"},
		},
	}}
	s := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false))
	RegisterAll(s, &Deps{Authorizer: az, Grafana: gf})
	return s
}

// callTool resolves the tool by name on s and invokes its handler with the
// given args. Fails the test if the tool is not registered or the handler
// returns a Go error (distinct from a structured IsError result, which the
// caller is expected to assert on themselves).
func callTool(t *testing.T, s *mcpsrv.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool := s.GetTool(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
	res, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("tool %q handler returned Go error: %v", name, err)
	}
	if res == nil {
		t.Fatalf("tool %q returned nil result", name)
	}
	return res
}

// resultText concatenates every TextContent on the result into one string.
// Most tools return a single TextContent; silences/panels may return a few.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestHandler_SearchDashboards wires the search_dashboards tool end-to-end:
// asserts the right Grafana path is hit with the caller's org-id header,
// and that the JSON response is grouped by folder before being handed back
// to the LLM.
func TestHandler_SearchDashboards(t *testing.T) {
	var sawPath, sawOrgID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawOrgID = r.Header.Get("X-Grafana-Org-Id")
		_, _ = w.Write([]byte(`[
			{"uid":"abc","title":"API Latency","folderTitle":"Platform","url":"/d/abc/api"},
			{"uid":"def","title":"Nodes","folderTitle":"Kubernetes","url":"/d/def/nodes"},
			{"uid":"ghi","title":"Root","folderTitle":"","url":"/d/ghi/root"}
		]`))
	}))
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "search_dashboards", map[string]any{"org": "acme"})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if sawPath != "/api/search" {
		t.Errorf("Grafana path = %q, want /api/search", sawPath)
	}
	if sawOrgID != "1" {
		t.Errorf("X-Grafana-Org-Id = %q, want 1", sawOrgID)
	}
	body := resultText(res)
	for _, want := range []string{"Platform", "Kubernetes", "API Latency", "Nodes"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q: %s", want, body)
		}
	}
}

// TestHandler_GetDashboardByUID covers the get_dashboard_by_uid tool — the
// simplest fetcher in the surface, but the one a regression in uid-escaping
// or org-header forwarding would first surface in.
func TestHandler_GetDashboardByUID(t *testing.T) {
	var sawPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		_, _ = w.Write([]byte(`{"dashboard":{"uid":"abc","title":"T","panels":[]}}`))
	}))
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "get_dashboard_by_uid", map[string]any{
		"org": "acme", "uid": "abc",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if sawPath != "/api/dashboards/uid/abc" {
		t.Errorf("path = %q, want /api/dashboards/uid/abc", sawPath)
	}
	if !strings.Contains(resultText(res), `"uid":"abc"`) {
		t.Errorf("body missing uid: %s", resultText(res))
	}
}

// TestHandler_QueryPrometheusHistogram proves the synthesis path: the
// handler composes histogram_quantile over the supplied metric/matchers/
// window/groupBy and dispatches it as a single Grafana datasource-proxy
// call. A subtle change in buildHistogramQuantile would slip past the
// helper-level test because the handler layer does not re-validate the
// PromQL — this test catches that class of regression.
func TestHandler_QueryPrometheusHistogram(t *testing.T) {
	var sawPath, sawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "query_prometheus_histogram", map[string]any{
		"org":      "acme",
		"metric":   "http_request_duration_seconds_bucket",
		"quantile": 0.95,
		"window":   "5m",
		"matchers": `job="api"`,
		"groupBy":  "route",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	// Proxy should route through the Mimir datasource (ID 10 per fixture).
	if !strings.HasPrefix(sawPath, "/api/datasources/proxy/10/api/v1/query") {
		t.Errorf("path = %q, want /api/datasources/proxy/10/api/v1/query*", sawPath)
	}
	// Synthesized PromQL must carry every user-supplied piece.
	for _, want := range []string{
		"histogram_quantile(0.95",
		"http_request_duration_seconds_bucket",
		`job="api"`,
		"rate(",
		"[5m]",
		"sum by (le, route)",
	} {
		if !strings.Contains(sawQuery, want) {
			t.Errorf("synthesized PromQL missing %q:\n%s", want, sawQuery)
		}
	}
}

// TestHandler_RunPanelQuery exercises run_panel_query, the tool whose full
// flow (dashboard fetch → panel parse → datasource kind resolve → proxy
// dispatch → expr forwarding) is impossible to cover from pure helpers.
// This is the single-highest-ROI test in the file — a regression anywhere
// along the chain surfaces here.
func TestHandler_RunPanelQuery(t *testing.T) {
	const dashboard = `{
		"dashboard": {
			"panels": [{
				"id": 1,
				"type": "timeseries",
				"datasource": {"type": "prometheus", "uid": "mimir-acme"},
				"targets": [{"refId": "A", "expr": "rate(http_requests_total[5m])"}]
			}],
			"templating": {"list": []}
		}
	}`

	var sawProxyQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/dashboards/uid/"):
			_, _ = w.Write([]byte(dashboard))
		case strings.HasPrefix(r.URL.Path, "/api/datasources/proxy/10/api/v1/query"):
			sawProxyQuery = r.URL.Query().Get("query")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "run_panel_query", map[string]any{
		"org":     "acme",
		"uid":     "dash1",
		"panelId": 1,
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if !strings.Contains(sawProxyQuery, "rate(http_requests_total[5m])") {
		t.Errorf("proxy query missing panel expr: %q", sawProxyQuery)
	}
}

// TestHandler_ListSilences covers the Alertmanager silences tool: tenant
// gating (needs TenantTypeAlerting), datasource selection (alertmanager-*),
// and the AM v2 projection that collapses matcher shapes into a single
// readable string form.
func TestHandler_ListSilences(t *testing.T) {
	var sawPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		_, _ = w.Write([]byte(`[
			{
				"id":"s1",
				"status":{"state":"active"},
				"startsAt":"2026-01-01T00:00:00Z",
				"endsAt":"2026-01-01T01:00:00Z",
				"createdBy":"alice",
				"comment":"deploy",
				"matchers":[{"name":"alertname","value":"Down","isRegex":false,"isEqual":true}]
			},
			{
				"id":"s2",
				"status":{"state":"expired"},
				"startsAt":"2025-01-01T00:00:00Z",
				"endsAt":"2025-01-02T00:00:00Z",
				"createdBy":"bob",
				"comment":"old",
				"matchers":[]
			}
		]`))
	}))
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "list_silences", map[string]any{"org": "acme"})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	// Proxy must route through the alertmanager datasource (id 13 per fixture)
	// with the AM v2 silences path.
	if !strings.HasPrefix(sawPath, "/api/datasources/proxy/13/alertmanager/api/v2/silences") {
		t.Errorf("path = %q, want /api/datasources/proxy/13/alertmanager/api/v2/silences*", sawPath)
	}
	body := resultText(res)
	// Default filter is state=active — expired silence should be filtered out,
	// active silence should come back with the projected matcher string.
	if !strings.Contains(body, `"id":"s1"`) {
		t.Errorf("active silence missing: %s", body)
	}
	if strings.Contains(body, `"id":"s2"`) {
		t.Errorf("expired silence should be filtered out by default: %s", body)
	}
	// Matcher is projected as alertname="Down" (quotes are JSON-escaped in the
	// emitted body; match the raw-string form so the test doesn't care
	// which quote character ends up in the wire payload).
	if !strings.Contains(body, `alertname=\"Down\"`) {
		t.Errorf("matcher projection missing: %s", body)
	}
}
