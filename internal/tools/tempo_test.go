package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/authz/authztest"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

const (
	testTempoUID  = "u-tempo"
	testTempoTool = "traceql-search"
	testJWTHeader = "X-JWT-Assertion"
)

// fakeTempoGrafana serves a real mcp-go MCP server at Grafana's Tempo
// datasource-proxy path, recording the JWT header each request carried.
func fakeTempoGrafana(t *testing.T, gotJWT *atomic.Value) *httptest.Server {
	t.Helper()
	tempo := mcpsrv.NewMCPServer("tempo", "0", mcpsrv.WithToolCapabilities(false))
	tempo.AddTool(mcp.NewTool(testTempoTool, mcp.WithDescription("stub")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	path := "/api/datasources/proxy/uid/" + testTempoUID + tempoMCPPath
	tempoHTTP := mcpsrv.NewStreamableHTTPServer(tempo, mcpsrv.WithEndpointPath(path))
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJWT.Store(r.Header.Get(testJWTHeader))
		tempoHTTP.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func listTools(ctx context.Context, s *mcpsrv.MCPServer) {
	msg := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	_ = s.HandleMessage(ctx, msg)
}

// In JWT auth mode there is no credential at startup: Tempo tools must
// be registered on the first tools/list that carries a caller token,
// using that caller's token for discovery.
func TestRegisterTempoTools_JWT_DeferredToFirstCaller(t *testing.T) {
	var gotJWT atomic.Value
	ts := fakeTempoGrafana(t, &gotJWT)
	org := orgFixture()
	gc := &fakeGrafana{listDS: []grafana.Datasource{{ID: 3, UID: testTempoUID, Name: "tempo", Type: "tempo"}}}
	b, err := newGFBinder(&authztest.Fake{Org: org}, gc, ts.URL, GrafanaAuth{JWTHeader: testJWTHeader}, nil)
	if err != nil {
		t.Fatalf("newGFBinder: %v", err)
	}
	s := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(true), mcpsrv.WithHooks(&mcpsrv.Hooks{}))

	if err := registerTempoTools(context.Background(), s, slog.Default(), b, staticOrgLister{orgs: []authz.Organization{org}}); err != nil {
		t.Fatalf("registerTempoTools: %v", err)
	}
	if s.GetTool(testTempoTool) != nil {
		t.Fatal("Tempo tool registered at startup in JWT mode; want deferred")
	}

	callerCtx := oauthCtx("sub-123", "alice@example.com")
	listTools(callerCtx, s)
	if s.GetTool(testTempoTool) != nil {
		t.Fatal("Tempo tool registered by a caller without a token")
	}

	listTools(grafana.WithUserToken(callerCtx, "id-token-alice"), s)
	if s.GetTool(testTempoTool) == nil {
		t.Fatal("Tempo tool not registered after first caller with a token")
	}
	if got, _ := gotJWT.Load().(string); got != "id-token-alice" {
		t.Errorf("Tempo dial %s = %q, want id-token-alice", testJWTHeader, got)
	}
	if gc.gotList.Caller != "alice@example.com" || gc.gotList.OrgID != org.OrgID {
		t.Errorf("seed ListDatasources opts = %+v, want caller alice@example.com in org %d", gc.gotList, org.OrgID)
	}
}

func TestTempoDiscovery_RetriesAfterBackoff(t *testing.T) {
	var calls int
	ok := false
	now := time.Unix(0, 0)
	d := &tempoDiscovery{
		discover: func(context.Context) bool { calls++; return ok },
		now:      func() time.Time { return now },
	}
	ctx := grafana.WithUserToken(context.Background(), "tok")

	d.run(ctx)
	d.run(ctx) // within backoff: skipped
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 within backoff", calls)
	}
	now = now.Add(tempoDiscoveryRetry)
	ok = true
	d.run(ctx)
	d.run(ctx) // done: skipped
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry, then done)", calls)
	}
}
