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
	"testing"
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
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "URL is required"},
		{Config{URL: "x"}, "Token or BasicAuth"},
		{Config{URL: "x", Token: "t", BasicAuth: "a:b"}, "only one of"},
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

// TestClient_LookupDatasourceUIDByID covers the GET /api/datasources/{id}
// flow that resolves a numeric ID into a UID. Three branches matter:
// happy path returns uid; an empty uid in the response is rejected;
// a 404 propagates as error (not silently empty — a missing datasource
// at lookup time is a real failure).
func TestClient_LookupDatasourceUIDByID(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantUID string
		wantErr string
	}{
		{"happy", http.StatusOK, `{"uid":"abc-123","name":"mimir"}`, "abc-123", ""},
		{"missing_uid", http.StatusOK, `{"name":"mimir"}`, "", "no uid"},
		{"not_found", http.StatusNotFound, `{"message":"not found"}`, "", "404"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath string
			ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			defer ts.Close()

			uid, err := client.LookupDatasourceUIDByID(context.Background(), RequestOpts{OrgID: 7}, 42)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if uid != c.wantUID {
				t.Errorf("uid = %q, want %q", uid, c.wantUID)
			}
			if gotPath != "/api/datasources/42" {
				t.Errorf("path = %q, want /api/datasources/42", gotPath)
			}
		})
	}
}

// TestClient_LookupDatasourceUIDByID_RejectsBadID guards the cheap input
// validation: a non-positive ID would otherwise produce /api/datasources/0
// or /api/datasources/-1, both of which Grafana 404s on, but the error
// would be confusing.
func TestClient_LookupDatasourceUIDByID_RejectsBadID(t *testing.T) {
	c, _ := New(Config{URL: "http://x", Token: "t"})
	if _, err := c.LookupDatasourceUIDByID(context.Background(), RequestOpts{}, 0); err == nil {
		t.Fatal("expected error for id=0")
	}
	if _, err := c.LookupDatasourceUIDByID(context.Background(), RequestOpts{}, -5); err == nil {
		t.Fatal("expected error for id=-5")
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
