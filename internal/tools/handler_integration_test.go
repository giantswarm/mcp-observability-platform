// Handler integration tests for the five hottest tools. Each wires a real
// grafana.Client against an httptest.Server that mocks the relevant Grafana
// endpoints, uses a fake Authorizer, invokes the tool's handler directly
// via MCPServer.GetTool, and asserts on both the downstream HTTP shape and
// the tool result. Catches the orchestration bugs (arg parsing, org
// resolution, response projection) that pure-helper tests miss.
package tools

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// emptyOrgLister returns no orgs, so the Tempo binder finds no seed
// datasource and skips registration. Tempo's MCP server isn't runnable
// in-process; integration coverage of the binder is the deploy-time
// smoke test in the PR description.
var emptyOrgLister = staticOrgLister{}

// newGrafanaJSONServer wraps handler with a default Content-Type:
// application/json so the production fetchJSON content-type guard is
// satisfied. Handlers that want to assert non-JSON responses set the
// Content-Type before writing.
func newGrafanaJSONServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		handler(w, r)
	}))
}

// wireHandlerTest builds the full tool surface against an httptest.Server.
// Returns the MCPServer (use GetTool to retrieve a handler) and a cleanup.
// Uses authztest.Fake to bypass Grafana user/org lookup (covered by authz
// tests) and hand every caller a fully-populated Organization — so tests
// focus on the tool-handler side of the pipeline.
func wireHandlerTest(t *testing.T, ts *httptest.Server) *mcpsrv.MCPServer {
	t.Helper()
	gf, err := grafana.New(grafana.Config{URL: ts.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("grafana.New: %v", err)
	}
	az := &authztest.Fake{Org: authz.Organization{
		Name:        "acme",
		DisplayName: "Acme",
		OrgID:       1,
		Role:        authz.RoleAdmin,
		Tenants: []authz.Tenant{{
			Name:  "acme",
			Types: []authz.TenantType{authz.TenantTypeData, authz.TenantTypeAlerting},
		}},
	}}
	s := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false))
	if err := RegisterAll(context.Background(), s, slog.Default(), az, emptyOrgLister, gf, ts.URL, "test-token", nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
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

// callToolWithCtx is the variant of callTool that lets the caller supply a
// context — useful for the authz-non-bypass tests below, where the
// integration test must put a UserInfo on the context so the real
// Authorizer can derive the caller (the framework-level RequireCaller
// middleware otherwise blocks the call before the handler runs).
func callToolWithCtx(t *testing.T, ctx context.Context, s *mcpsrv.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool := s.GetTool(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
	res, err := tool.Handler(ctx, req)
	if err != nil {
		t.Fatalf("tool %q handler returned Go error: %v", name, err)
	}
	if res == nil {
		t.Fatalf("tool %q returned nil result", name)
	}
	return res
}

// TestHandler_SearchDashboards wires the search_dashboards tool end-to-end:
// asserts the right Grafana path is hit with the caller's org-id header,
// and that the JSON response is grouped by folder before being handed back
// to the LLM.
func TestHandler_SearchDashboards(t *testing.T) {
	var sawPath, sawOrgID string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawOrgID = r.Header.Get("X-Grafana-Org-Id")
		_, _ = w.Write([]byte(`[
			{"uid":"abc","title":"API Latency","folderTitle":"Platform","url":"/d/abc/api"},
			{"uid":"def","title":"Nodes","folderTitle":"Kubernetes","url":"/d/def/nodes"},
			{"uid":"ghi","title":"Root","folderTitle":"","url":"/d/ghi/root"}
		]`))
	})
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

// TestHandler_ListDatasources_PropagatesOrgID is the regression test for
// the multi-org bug fix: the local list_datasources handler MUST send
// X-Grafana-Org-Id from the caller's resolved OrgID. Upstream
// mcp-grafana's openapi-based handler dropped this header, so this test
// pins the new behaviour.
func TestHandler_ListDatasources_PropagatesOrgID(t *testing.T) {
	var sawPath, sawOrgID string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawOrgID = r.Header.Get("X-Grafana-Org-Id")
		_, _ = w.Write([]byte(`[
			{"id":1,"uid":"mimir-uid","name":"GS Mimir","type":"prometheus","isDefault":true},
			{"id":2,"uid":"loki-uid","name":"GS Loki","type":"loki","isDefault":false},
			{"id":3,"uid":"tempo-uid","name":"GS Tempo","type":"tempo","isDefault":false}
		]`))
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "list_datasources", map[string]any{
		"org": "acme", "type": "loki",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if sawPath != "/api/datasources" {
		t.Errorf("Grafana path = %q, want /api/datasources", sawPath)
	}
	if sawOrgID != "1" {
		t.Errorf("X-Grafana-Org-Id = %q, want %q (the resolved OrgID for caller's org)", sawOrgID, "1")
	}
	body := resultText(res)
	// Type filter applied; only the loki entry should remain.
	if !strings.Contains(body, "loki-uid") {
		t.Errorf("response missing loki entry: %s", body)
	}
	if strings.Contains(body, "mimir-uid") || strings.Contains(body, "tempo-uid") {
		t.Errorf("type filter did not exclude non-loki entries: %s", body)
	}
}

// TestHandler_GetDashboardByUID covers the get_dashboard_by_uid tool — the
// simplest fetcher in the surface, but the one a regression in uid-escaping
// or org-header forwarding would first surface in.
func TestHandler_GetDashboardByUID(t *testing.T) {
	var sawPath string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		_, _ = w.Write([]byte(`{"dashboard":{"uid":"abc","title":"T","panels":[]}}`))
	})
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

// alertmanagerListResp is the live /api/datasources payload the live-fetch
// resolver expects: one alertmanager-typed datasource with a known id so
// the proxy URL is predictable.
const alertmanagerListResp = `[{"id":42,"uid":"am-acme","name":"alertmanager-acme","type":"alertmanager"}]`

// TestHandler_ListSilences asserts live datasource resolution, the AM v2
// proxy path, server-side filter passthrough, default-state filter (active
// only), and minimal projection.
func TestHandler_ListSilences(t *testing.T) {
	var sawPath, sawFilter string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasources":
			_, _ = w.Write([]byte(alertmanagerListResp))
		default:
			sawPath = r.URL.Path
			sawFilter = r.URL.Query().Get("filter")
			_, _ = w.Write([]byte(`[
				{"id":"s-active","status":{"state":"active"},"endsAt":"2026-05-02T00:00:00Z","createdBy":"alice","matchers":[{"name":"alertname","value":"X","isRegex":false,"isEqual":true}],"comment":"c1"},
				{"id":"s-pending","status":{"state":"pending"},"endsAt":"2026-06-01T00:00:00Z","createdBy":"bob","matchers":[],"comment":""},
				{"id":"s-expired","status":{"state":"expired"},"endsAt":"2026-04-01T00:00:00Z","createdBy":"carol","matchers":[],"comment":""}
			]`))
		}
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "list_silences", map[string]any{
		"org":     "acme",
		"matcher": `alertname="X"`,
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if sawPath != "/api/datasources/proxy/42/alertmanager/api/v2/silences" {
		t.Errorf("path = %q, want AM v2 silences via DS proxy at id 42", sawPath)
	}
	if sawFilter != `alertname="X"` {
		t.Errorf("filter query = %q, want alertname=\"X\"", sawFilter)
	}
	body := resultText(res)
	if !strings.Contains(body, "s-active") {
		t.Errorf("default state should include active silence: %s", body)
	}
	if strings.Contains(body, "s-pending") || strings.Contains(body, "s-expired") {
		t.Errorf("default state should not include pending/expired: %s", body)
	}
}

// TestHandler_GetSilence asserts the singular AM v2 path
// /api/v2/silence/{id} (not silences) and full-record passthrough.
func TestHandler_GetSilence(t *testing.T) {
	var sawPath string
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasources":
			_, _ = w.Write([]byte(alertmanagerListResp))
		default:
			sawPath = r.URL.Path
			_, _ = w.Write([]byte(`{"id":"abc","status":{"state":"active"},"startsAt":"2026-04-30T00:00:00Z","endsAt":"2026-05-01T00:00:00Z","createdBy":"alice","comment":"hush","matchers":[{"name":"alertname","value":"X","isEqual":true}]}`))
		}
	})
	defer ts.Close()

	res := callTool(t, wireHandlerTest(t, ts), "get_silence", map[string]any{
		"org": "acme",
		"id":  "abc",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", resultText(res))
	}
	if sawPath != "/api/datasources/proxy/42/alertmanager/api/v2/silence/abc" {
		t.Errorf("path = %q, want singular silence/{id} path at id 42", sawPath)
	}
	body := resultText(res)
	for _, want := range []string{`"id":"abc"`, `"createdBy":"alice"`, `"comment":"hush"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

// TestHandler_RegistersNewTools is a low-cost guard that the new tools
// from roadmap §0 actually reach the MCP server's tool registry. Catches
// a missed wire-in in tools.RegisterAll.
func TestHandler_RegistersNewTools(t *testing.T) {
	ts := newGrafanaJSONServer(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	defer ts.Close()
	s := wireHandlerTest(t, ts)
	for _, name := range []string{"run_panel_query", "get_query_examples", "list_silences", "get_silence", "get_panel_image"} {
		if s.GetTool(name) == nil {
			t.Errorf("tool %q not registered", name)
		}
	}
}
