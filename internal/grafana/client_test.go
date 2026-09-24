package grafana

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer wraps handler with a default Content-Type: application/json
// so individual tests don't have to thread the header. Handlers that want
// to assert non-JSON behaviour set Content-Type before writing.
func newTestServer(handler http.HandlerFunc) (*httptest.Server, Client) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		handler(w, r)
	}))
	c, _ := New(Config{URL: ts.URL, Token: "test-token"})
	return ts, c
}

func TestClient_AuthHeader_Bearer(t *testing.T) {
	var gotAuth string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("{}"))
	})
	defer ts.Close()

	_, err := c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 5}, 1, "api/v1/query", nil)
	if err != nil {
		t.Fatalf("DatasourceProxy: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth header = %q, want 'Bearer test-token'", gotAuth)
	}
}

func TestClient_AuthHeader_Basic(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	c, err := New(Config{URL: ts.URL, BasicAuth: "admin:pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 1, "api/v1/query", nil)
	if err != nil {
		t.Fatalf("DatasourceProxy: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:pw"))
	if gotAuth != want {
		t.Errorf("basic auth = %q, want %q", gotAuth, want)
	}
}

const testJWTHeader = "X-JWT-Assertion"

// newJWTTestServer is newTestServer in JWT auth mode.
func newJWTTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, Client) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	c, err := New(Config{URL: ts.URL, JWTHeader: testJWTHeader})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ts, c
}

func TestClient_JWT_ForwardsCallerToken(t *testing.T) {
	var gotJWT, gotAuth string
	_, c := newJWTTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotJWT = r.Header.Get(testJWTHeader)
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("{}"))
	})

	ctx := WithUserToken(context.Background(), "id-token-alice")
	if _, err := c.DatasourceProxy(ctx, RequestOpts{OrgID: 1}, 1, "api/v1/query", nil); err != nil {
		t.Fatalf("DatasourceProxy: %v", err)
	}
	if gotJWT != "id-token-alice" {
		t.Errorf("%s = %q, want id-token-alice", testJWTHeader, gotJWT)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none in JWT mode", gotAuth)
	}
}

func TestClient_JWT_NoTokenSendsNothing(t *testing.T) {
	var hits atomic.Int64
	_, c := newJWTTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("[]"))
	})

	_, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1})
	if !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("err = %v, want ErrNoUserToken", err)
	}
	if _, err := c.CurrentUserOrgs(context.Background()); !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("CurrentUserOrgs err = %v, want ErrNoUserToken", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("upstream hit %d times, want 0 without a caller token", got)
	}
}

func TestClient_CurrentUserOrgs(t *testing.T) {
	var gotPath, gotOrg string
	_, c := newJWTTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotOrg = r.Header.Get("X-Grafana-Org-Id")
		_, _ = w.Write([]byte(`[{"orgId":1,"name":"Main","role":"Viewer"},{"orgId":7,"name":"acme","role":"Admin"}]`))
	})

	got, err := c.CurrentUserOrgs(WithUserToken(context.Background(), "tok"))
	if err != nil {
		t.Fatalf("CurrentUserOrgs: %v", err)
	}
	if gotPath != "/api/user/orgs" {
		t.Errorf("path = %q, want /api/user/orgs", gotPath)
	}
	if gotOrg != "" {
		t.Errorf("X-Grafana-Org-Id = %q, want none", gotOrg)
	}
	want := []UserOrgMembership{{OrgID: 1, Role: "Viewer"}, {OrgID: 7, Role: "Admin"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("orgs = %+v, want %+v", got, want)
	}
}

func TestClient_CurrentUserOrgs_Unauthorized(t *testing.T) {
	_, c := newJWTTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid JWT"}`))
	})

	_, err := c.CurrentUserOrgs(WithUserToken(context.Background(), "tok"))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestClient_OrgIDAndCallerHeaders(t *testing.T) {
	var gotOrg, gotUser string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotOrg = r.Header.Get("X-Grafana-Org-Id")
		gotUser = r.Header.Get("X-Grafana-User")
		_, _ = w.Write([]byte("{}"))
	})
	defer ts.Close()

	opts := RequestOpts{OrgID: 42, Caller: "alice@example.com"}
	_, err := c.DatasourceProxy(context.Background(), opts, 1, "api/v1/query", nil)
	if err != nil {
		t.Fatalf("DatasourceProxy: %v", err)
	}
	if gotOrg != "42" {
		t.Errorf("X-Grafana-Org-Id = %q, want 42", gotOrg)
	}
	if gotUser != "alice@example.com" {
		t.Errorf("X-Grafana-User = %q, want alice@example.com", gotUser)
	}
}

func TestClient_OmitsOrgIdWhenZero(t *testing.T) {
	// /api/orgs is called with orgID=0 during VerifyServerAdmin;
	// the X-Grafana-Org-Id header must NOT be set in that case.
	var sawHeader bool
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Grafana-Org-Id") != "" {
			sawHeader = true
		}
		_, _ = w.Write([]byte("[]"))
	})
	defer ts.Close()

	if err := c.VerifyServerAdmin(context.Background()); err != nil {
		t.Fatalf("VerifyServerAdmin: %v", err)
	}
	if sawHeader {
		t.Errorf("X-Grafana-Org-Id must not be set when OrgID=0")
	}
}

func TestClient_VerifyServerAdmin_Unauthorised(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer ts.Close()
	if err := c.VerifyServerAdmin(context.Background()); err == nil {
		t.Errorf("expected error on 403")
	}
}

func TestClient_DetectsPrometheusErrorIn200(t *testing.T) {
	// Prometheus returns status=error in a 200 body on malformed queries.
	// The client must treat this as an error.
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid query"}`))
	})
	defer ts.Close()

	_, err := c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 5, "api/v1/query", url.Values{"query": []string{"bad"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad_data") || !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("error should carry errorType + error fields, got: %v", err)
	}
}

func TestClient_DatasourceProxy_PathAndQuery(t *testing.T) {
	var gotPath, gotQuery string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte("{}"))
	})
	defer ts.Close()

	q := url.Values{"query": []string{"up"}, "start": []string{"1"}, "end": []string{"2"}}
	_, err := c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 7, "api/v1/query_range", q)
	if err != nil {
		t.Fatalf("DatasourceProxy: %v", err)
	}
	if gotPath != "/api/datasources/proxy/7/api/v1/query_range" {
		t.Errorf("path = %q", gotPath)
	}
	// url.Values encodes alphabetically; assert all args are present.
	for _, want := range []string{"query=up", "start=1", "end=2"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %s", gotQuery, want)
		}
	}
}

func TestNew_Validation(t *testing.T) {
	const onlyOne = "only one of"
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "URL is required"},
		{Config{URL: "x"}, "Token, BasicAuth or JWTHeader"},
		{Config{URL: "x", Token: "t", BasicAuth: "a:b"}, onlyOne},
		{Config{URL: "x", Token: "t", JWTHeader: testJWTHeader}, onlyOne},
		{Config{URL: "x", BasicAuth: "a:b", JWTHeader: testJWTHeader}, onlyOne},
	}
	for _, c := range cases {
		_, err := New(c.cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%+v) = %v, want substring %q", c.cfg, err, c.want)
		}
	}
}

func TestValidateDatasourceProxyPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"happy prometheus", "api/v1/query_range", false},
		{"happy loki", "loki/api/v1/query_range", false},
		{"happy tempo", "api/v2/search/tags", false},
		{"empty", "", true},
		{"leading slash", "/api/v1/query", true},
		{"contains ..", "api/../admin", true},
		{"double dots mid-path", "api/v1/../../etc/passwd", true},
		{"url-encoded dot-dot", "api/v1/%2e%2e/admin", true},
		{"url-encoded dot-dot uppercase", "api/v1/%2E%2E/admin", true},
		{"invalid url escape", "api/v1/%zz", true},
		{"too long", strings.Repeat("a", 1025), true},
		{"just under limit", strings.Repeat("a", 1024), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDatasourceProxyPath(c.path)
			if c.wantErr && err == nil {
				t.Errorf("validateDatasourceProxyPath(%q) = nil, want error", c.path)
			}
			if !c.wantErr && err != nil {
				t.Errorf("validateDatasourceProxyPath(%q) = %v, want nil", c.path, err)
			}
			if err != nil && !errors.Is(err, errInvalidDatasourceProxyPath) {
				t.Errorf("validateDatasourceProxyPath(%q) error does not wrap errInvalidDatasourceProxyPath: %v", c.path, err)
			}
		})
	}
}

func TestDatasourceProxy_RejectsInvalidPath(t *testing.T) {
	// The server must NOT be reached when the path is invalid — this test
	// gives the handler a hook to flip a bool if it fires and asserts false.
	var hit bool
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte("{}"))
	})
	defer ts.Close()

	_, err := c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 5, "api/../secret", nil)
	if err == nil {
		t.Fatalf("expected error on traversal path")
	}
	if !errors.Is(err, errInvalidDatasourceProxyPath) {
		t.Errorf("err does not wrap errInvalidDatasourceProxyPath: %v", err)
	}
	if hit {
		t.Errorf("upstream should not be hit on invalid path")
	}
}

func TestDoGET_CapsResponseBody(t *testing.T) {
	// Write more than maxResponseBytes; the client must refuse, not OOM.
	huge := bytes.Repeat([]byte("A"), maxResponseBytes+1024)
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(huge)
	})
	defer ts.Close()

	_, err := c.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 1, "api/v1/query", nil)
	if err == nil {
		t.Fatalf("expected size-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should mention size cap, got: %v", err)
	}
}

// TestClient_ErrorStatusCodes_SurfaceUpstreamBody covers the Grafana-side
// error shapes the MCP actually has to reason about (401/403/429/500/502/503).
// The client must: return a non-nil error, preserve the status code in the
// message, and fold in the upstream body so operators can triage.
func TestClient_ErrorStatusCodes_SurfaceUpstreamBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"401_unauthorized", http.StatusUnauthorized, `{"message":"Unauthorized"}`},
		{"403_forbidden", http.StatusForbidden, `{"message":"Access denied"}`},
		{"429_rate_limited", http.StatusTooManyRequests, `{"message":"too many requests"}`},
		{"500_internal", http.StatusInternalServerError, `{"message":"internal server error"}`},
		{"502_bad_gateway", http.StatusBadGateway, `bad gateway`},
		{"503_unavailable", http.StatusServiceUnavailable, `service unavailable`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			defer ts.Close()

			_, err := client.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 1, "api/v1/query", nil)
			if err == nil {
				t.Fatalf("expected error for status %d", c.status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("status %d", c.status)) {
				t.Errorf("error should mention status %d, got: %v", c.status, err)
			}
			if !strings.Contains(err.Error(), c.body) {
				t.Errorf("error should include upstream body (%q), got: %v", c.body, err)
			}
		})
	}
}

// TestClient_LookupUser_ErrorPaths covers the Grafana /api/users/lookup
// variations that matter for the authz flow: 404 → (nil, nil) means
// "user not provisioned yet"; 401/403/5xx must error (they are NOT silent
// "user doesn't exist" — a denial vs a miss is security-relevant).
func TestClient_LookupUser_ErrorPaths(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantNil   bool
		wantError bool
	}{
		{"404_user_not_provisioned", http.StatusNotFound, true, false},
		{"401_auth_failure", http.StatusUnauthorized, false, true},
		{"403_forbidden", http.StatusForbidden, false, true},
		{"500_upstream", http.StatusInternalServerError, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(`{"message":"err"}`))
			})
			defer ts.Close()

			u, err := client.LookupUser(context.Background(), "alice@example.com")
			if c.wantError && err == nil {
				t.Fatalf("expected error for status %d", c.status)
			}
			if !c.wantError && err != nil {
				t.Fatalf("unexpected error for status %d: %v", c.status, err)
			}
			if c.wantNil && u != nil {
				t.Errorf("expected nil user for status %d, got %+v", c.status, u)
			}
		})
	}
}

// TestClient_ErrorBodyCapped proves the error-path readLimited call obeys
// the body cap. A compromised or misbehaving upstream returning a multi-GiB
// error response must not OOM the MCP.
func TestClient_ErrorBodyCapped(t *testing.T) {
	huge := bytes.Repeat([]byte("X"), maxResponseBytes+1024)
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(huge)
	})
	defer ts.Close()

	_, err := client.DatasourceProxy(context.Background(), RequestOpts{OrgID: 1}, 1, "api/v1/query", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	// readLimited returns its own size-cap error; the client wraps it, so
	// either signal is acceptable — what we need is that no crash / hang
	// / unbounded allocation happened.
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("expected body-cap error, got: %v", err)
	}
}

func TestClient_ListDatasources_ParsesManageAlertsAndForwardsOrg(t *testing.T) {
	body := `[
		{"id":1,"uid":"u1","name":"prom","type":"prometheus","jsonData":{"manageAlerts":true}},
		{"id":2,"uid":"u2","name":"loki","type":"loki","jsonData":{"manageAlerts":false}},
		{"id":3,"uid":"u3","name":"prom-default","type":"prometheus","jsonData":{}},
		{"id":4,"uid":"u4","name":"prom-no-jsondata","type":"prometheus"}
	]`
	var gotPath, gotOrg string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotOrg = r.Header.Get("X-Grafana-Org-Id")
		_, _ = w.Write([]byte(body))
	})
	defer ts.Close()

	got, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 7})
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	if gotPath != "/api/datasources" {
		t.Errorf("path = %q, want /api/datasources", gotPath)
	}
	if gotOrg != "7" {
		t.Errorf("X-Grafana-Org-Id = %q, want 7", gotOrg)
	}
	want := []Datasource{
		{ID: 1, UID: "u1", Name: "prom", Type: string(DSTypePrometheus), ManageAlerts: true},
		{ID: 2, UID: "u2", Name: "loki", Type: string(DSTypeLoki), ManageAlerts: false},
		{ID: 3, UID: "u3", Name: "prom-default", Type: string(DSTypePrometheus), ManageAlerts: true},
		{ID: 4, UID: "u4", Name: "prom-no-jsondata", Type: string(DSTypePrometheus), ManageAlerts: true},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClient_ListDatasources_ErrorOnNon2xx(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	})
	defer ts.Close()
	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err == nil {
		t.Fatal("expected error on 403, got nil")
	}
}

func TestClient_LookupDatasourceByUID(t *testing.T) {
	const listBody = `[
		{"id":1,"uid":"u1","name":"prom","type":"prometheus","jsonData":{"manageAlerts":true}},
		{"id":2,"uid":"u2","name":"loki","type":"loki","jsonData":{"manageAlerts":false}}
	]`
	cases := []struct {
		name    string
		uid     string
		body    string
		wantDS  Datasource
		wantErr string
	}{
		{
			name:   "hit",
			uid:    "u2",
			body:   listBody,
			wantDS: Datasource{ID: 2, UID: "u2", Name: "loki", Type: string(DSTypeLoki), ManageAlerts: false},
		},
		{
			name:    "miss with non-empty list",
			uid:     "unknown",
			body:    listBody,
			wantErr: `datasource "unknown" not found in org`,
		},
		{
			name:    "miss with empty list",
			uid:     "u1",
			body:    `[]`,
			wantErr: `datasource "u1" not found in org`,
		},
		{
			name:    "empty uid is rejected without HTTP call",
			uid:     "",
			body:    listBody,
			wantErr: "datasource uid is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			ts, c := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
				hits++
				_, _ = w.Write([]byte(tc.body))
			})
			defer ts.Close()
			got, err := c.LookupDatasourceByUID(context.Background(), RequestOpts{OrgID: 1}, tc.uid)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				if tc.uid == "" && hits != 0 {
					t.Errorf("empty uid must not hit Grafana; hits=%d", hits)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantDS {
				t.Errorf("ds = %+v, want %+v", got, tc.wantDS)
			}
		})
	}
}

// onePromDatasourceBody is the minimal /api/datasources response shared
// by the simple cache tests. Tests that need richer payloads (multiple
// datasources, manageAlerts variants) inline their own body.
const onePromDatasourceBody = `[{"id":1,"uid":"u1","name":"prom","type":"prometheus"}]`

// newCacheTestServer is newTestServer + a hit counter and a fixed-clock
// hook. It returns the *client so cache TTL / clock can be driven from
// the test deterministically.
func newCacheTestServer(t *testing.T, body string) (*httptest.Server, *client, *int64, *atomic.Int64) {
	t.Helper()
	var hits int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	c, err := New(Config{URL: ts.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	impl := c.(*client)
	var nowNs atomic.Int64
	nowNs.Store(time.Now().UnixNano())
	impl.now = func() time.Time { return time.Unix(0, nowNs.Load()) }
	return ts, impl, &hits, &nowNs
}

func TestClient_ListDatasources_CachesPerOrg(t *testing.T) {
	body := `[{"id":1,"uid":"u1","name":"prom","type":"prometheus","jsonData":{"manageAlerts":true}}]`
	ts, c, hits, _ := newCacheTestServer(t, body)
	defer ts.Close()

	for i := 0; i < 3; i++ {
		if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (TTL not honoured)", got)
	}
}

func TestClient_ListDatasources_CacheTTLExpiry(t *testing.T) {
	ts, c, hits, nowNs := newCacheTestServer(t, onePromDatasourceBody)
	defer ts.Close()

	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err != nil {
		t.Fatalf("first: %v", err)
	}
	nowNs.Add(int64(c.dsCacheTTL) + int64(time.Second))
	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err != nil {
		t.Fatalf("post-TTL: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Errorf("upstream hit %d times, want 2 (TTL did not expire)", got)
	}
}

const (
	testCallerA = "alice"
	testCallerB = "bob"
)

// A per-user result (JWT auth mode) must never serve another caller in
// the same org.
func TestClient_ListDatasources_CachePerCallerIsolation(t *testing.T) {
	ts, c, hits, _ := newCacheTestServer(t, onePromDatasourceBody)
	defer ts.Close()
	c.jwtHeader = "X-JWT-Assertion"

	for _, caller := range []string{testCallerA, testCallerB, testCallerA} {
		ctx := WithUserToken(context.Background(), caller+"-token")
		if _, err := c.ListDatasources(ctx, RequestOpts{OrgID: 1, Caller: caller}); err != nil {
			t.Fatalf("caller %s: %v", caller, err)
		}
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Errorf("upstream hit %d times, want 2 (per-caller isolation broken)", got)
	}
}

// With the shared SA credential every caller sees the same list, so
// callers in one org share one cache entry.
func TestClient_ListDatasources_CacheSharedAcrossCallersWithSA(t *testing.T) {
	ts, c, hits, _ := newCacheTestServer(t, onePromDatasourceBody)
	defer ts.Close()

	for _, caller := range []string{testCallerA, testCallerB} {
		if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1, Caller: caller}); err != nil {
			t.Fatalf("caller %s: %v", caller, err)
		}
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (SA mode shares the org entry)", got)
	}
}

func TestClient_ListDatasources_CachePerOrgIsolation(t *testing.T) {
	ts, c, hits, _ := newCacheTestServer(t, onePromDatasourceBody)
	defer ts.Close()

	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err != nil {
		t.Fatalf("org 1: %v", err)
	}
	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 2}); err != nil {
		t.Fatalf("org 2: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Errorf("upstream hit %d times, want 2 (per-org isolation broken)", got)
	}
}

// Errors must NOT poison the cache: a transient 5xx followed by a 200
// should result in two upstream hits (no stale-error replay) and the
// caller should see the success.
func TestClient_ListDatasources_ErrorNotCached(t *testing.T) {
	var hits int64
	var fail atomic.Bool
	fail.Store(true)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`upstream down`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(onePromDatasourceBody))
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "t"})

	if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1}); err == nil {
		t.Fatal("expected error from 502")
	}
	fail.Store(false)
	got, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1})
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if len(got) != 1 || got[0].UID != "u1" {
		t.Errorf("retry result = %+v, want u1", got)
	}
	if h := atomic.LoadInt64(&hits); h != 2 {
		t.Errorf("upstream hit %d times, want 2 (error was cached)", h)
	}
}

// LookupDatasourceByUID is implemented in terms of ListDatasources, so
// the cache must transparently apply: two consecutive lookups for the
// same org should result in a single /api/datasources fetch.
func TestClient_LookupDatasourceByUID_SharesCache(t *testing.T) {
	body := `[
		{"id":1,"uid":"u1","name":"prom","type":"prometheus"},
		{"id":2,"uid":"u2","name":"loki","type":"loki"}
	]`
	ts, c, hits, _ := newCacheTestServer(t, body)
	defer ts.Close()

	if _, err := c.LookupDatasourceByUID(context.Background(), RequestOpts{OrgID: 1}, "u1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := c.LookupDatasourceByUID(context.Background(), RequestOpts{OrgID: 1}, "u2"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (LookupDatasourceByUID does not reuse cache)", got)
	}
}

// Concurrent ListDatasources calls for the same org must not race the
// cache map. With -race this exercises the sync.Map path; without it
// the test still asserts no goroutine returned an error.
func TestClient_ListDatasources_ConcurrentSafe(t *testing.T) {
	ts, c, _, _ := newCacheTestServer(t, onePromDatasourceBody)
	defer ts.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(orgID int64) {
			defer wg.Done()
			if _, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: orgID}); err != nil {
				errs <- err
			}
		}(int64(i % 4))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call: %v", err)
	}
}

func TestRedactedHeader_DoesNotLeakInPrints(t *testing.T) {
	c, err := New(Config{URL: "http://example.invalid", Token: "super-secret-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Reach into the unexported concrete impl to exercise the redacted
	// String()/GoString() methods. Tests live in the same package, so
	// direct field access via assertion is fine.
	impl := c.(*client)
	for _, verb := range []string{"%v", "%s", "%+v", "%#v"} {
		s := fmt.Sprintf(verb, impl.authHeader)
		if strings.Contains(s, "super-secret-token") {
			t.Errorf("authHeader leaked via %s: %q", verb, s)
		}
		if !strings.Contains(s, "REDACTED") {
			t.Errorf("authHeader %s did not contain REDACTED: %q", verb, s)
		}
	}
}
