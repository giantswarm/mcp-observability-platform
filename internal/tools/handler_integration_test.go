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
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/tools/upstream"
)

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
		Datasources: []authz.Datasource{
			{ID: 10, Name: "mimir-acme"},
			{ID: 11, Name: "loki-acme"},
			{ID: 12, Name: "tempo-acme"},
			{ID: 13, Name: "alertmanager-acme"},
		},
	}}
	br, err := upstream.NewBridge(az, gf, ts.URL, "test-token", nil)
	if err != nil {
		t.Fatalf("upstream.NewBridge: %v", err)
	}
	s := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false))
	RegisterAll(s, az, gf, br)
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

