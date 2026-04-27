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

	_, err := c.GetDashboard(context.Background(), RequestOpts{OrgID: 5}, "uid123")
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
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
	_, err = c.ListDatasources(context.Background(), RequestOpts{OrgID: 1})
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
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
	_, err := c.SearchDashboards(context.Background(), opts, "", 10)
	if err != nil {
		t.Fatalf("SearchDashboards: %v", err)
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

func TestClient_RenderPanel_RendererMissing(t *testing.T) {
	// When Grafana's renderer is absent, the /render endpoint returns an
	// HTML error page. Our client must translate that into an actionable
	// error mentioning grafana-image-renderer.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>Rendering plugin is not installed</html>"))
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "t"})

	_, _, err := c.RenderPanel(context.Background(), RequestOpts{OrgID: 1}, "abc", 2, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "grafana-image-renderer") {
		t.Errorf("error should mention grafana-image-renderer, got: %v", err)
	}
}

func TestClient_RenderPanel_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "t"})

	body, ct, err := c.RenderPanel(context.Background(), RequestOpts{OrgID: 1}, "abc", 2, nil)
	if err != nil {
		t.Fatalf("RenderPanel: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.HasPrefix(string(body), "\x89PNG") {
		t.Errorf("body prefix = %q", body[:4])
	}
}

func TestClient_HasImageRenderer(t *testing.T) {
	// 200 = installed, 404 = not installed, anything else surfaces as an
	// error so callers don't treat a transient 5xx as "renderer missing".
	cases := []struct {
		name         string
		status       int
		wantPresent  bool
		wantErr      bool
		wantStatusIn string
	}{
		{"installed", http.StatusOK, true, false, ""},
		{"not installed", http.StatusNotFound, false, false, ""},
		{"transient 502 surfaces error", http.StatusBadGateway, false, true, "status 502"},
		{"perms error surfaces", http.StatusForbidden, false, true, "status 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer ts.Close()
			c, _ := New(Config{URL: ts.URL, Token: "t"})

			present, err := c.HasImageRenderer(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got present=%v nil", present)
				}
				if tc.wantStatusIn != "" && !strings.Contains(err.Error(), tc.wantStatusIn) {
					t.Errorf("error %q should mention %q", err, tc.wantStatusIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if present != tc.wantPresent {
				t.Errorf("present = %v, want %v", present, tc.wantPresent)
			}
		})
	}
}

func TestClient_HasImageRenderer_RecoversAfterTransientError(t *testing.T) {
	// Without the cache, a probe that fails once must not lock callers out
	// — the next call re-probes and observes recovery immediately.
	var attempts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "t"})

	if _, err := c.HasImageRenderer(context.Background()); err == nil {
		t.Fatal("first call: want error, got nil")
	}
	present, err := c.HasImageRenderer(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !present {
		t.Fatal("second call: present=false, want true after upstream recovery")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
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

	_, err := c.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "uid")
	if err == nil {
		t.Fatalf("expected size-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should mention size cap, got: %v", err)
	}
}

func TestSanitizeCallerHeader(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"alice@example.com", "alice@example.com"},
		{"user\r\nInjected: evil", "userInjected: evil"},
		{"\x00\x01\x02safe", "safe"},
		{strings.Repeat("x", 300), strings.Repeat("x", 256)},
		{"\t\n\r", ""},
		{"ü-non-ascii", "-non-ascii"}, // non-ASCII stripped
	}
	for _, c := range cases {
		got := sanitizeCallerHeader(c.in)
		if got != c.want {
			t.Errorf("sanitizeCallerHeader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCallerHeader_SanitisedOnWire(t *testing.T) {
	// End-to-end: caller contains CRLF; the value on the wire must be safe.
	var got string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Grafana-User")
		_, _ = w.Write([]byte("{}"))
	})
	defer ts.Close()

	_, err := c.ListDatasources(context.Background(), RequestOpts{OrgID: 1, Caller: "alice\r\nInjected: evil"})
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("X-Grafana-User still contains CR/LF: %q", got)
	}
	if got != "aliceInjected: evil" {
		t.Errorf("sanitised header = %q, want %q", got, "aliceInjected: evil")
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

			_, err := client.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "uid")
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

	_, err := client.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "uid")
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

// TestClient_SameOriginRedirect_Followed proves the redirect policy permits
// an in-cluster sidecar proxy (e.g. nginx / istio doing trailing-slash
// normalisation or path rewrites) to send a 301 back to the same host. The
// client must follow it and the second request must carry the original
// Authorization header (stdlib behaviour for same-origin).
func TestClient_SameOriginRedirect_Followed(t *testing.T) {
	var hits int
	var sawAuthOnFinal string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			// Simulate a sidecar trailing-slash normaliser.
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		sawAuthOnFinal = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "tok"})

	_, err := c.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "abc")
	if err != nil {
		t.Fatalf("GetDashboard through same-origin redirect: %v", err)
	}
	if hits != 2 {
		t.Errorf("expected 2 hits (original + redirect target), got %d", hits)
	}
	if sawAuthOnFinal != "Bearer tok" {
		t.Errorf("Authorization lost through same-origin redirect: got %q", sawAuthOnFinal)
	}
}

// TestClient_CrossOriginRedirect_Blocked proves the redirect policy rejects
// a 302 that would send the Authorization header to a different host —
// exactly the credential-leak vector the policy exists to close.
func TestClient_CrossOriginRedirect_Blocked(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If this ever fires, the policy failed — credentials just landed
		// at an attacker-controlled host.
		t.Errorf("evil server reached — cross-origin redirect was followed! authz=%q", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("{}"))
	}))
	defer evil.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/api/dashboards/uid/abc", http.StatusFound)
	}))
	defer origin.Close()
	c, _ := New(Config{URL: origin.URL, Token: "tok"})

	_, err := c.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "abc")
	if err == nil {
		t.Fatalf("expected cross-origin redirect to be rejected")
	}
	if !strings.Contains(err.Error(), "cross-origin") {
		t.Errorf("error should mention cross-origin redirect, got: %v", err)
	}
}

// TestClient_RedirectHopLimit prevents redirect loops / chains longer than
// the small hop budget. A sidecar bug looping on trailing-slash rewrites
// must surface as an error, not hang the MCP.
func TestClient_RedirectHopLimit(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, fmt.Sprintf("%s/loop?n=%d", r.Host, hits), http.StatusMovedPermanently)
	}))
	defer ts.Close()
	c, _ := New(Config{URL: ts.URL, Token: "tok"})

	_, err := c.GetDashboard(context.Background(), RequestOpts{OrgID: 1}, "abc")
	if err == nil {
		t.Fatalf("expected too-many-redirects error")
	}
	if hits > 5 {
		t.Errorf("hop budget leaked: saw %d hits before abort", hits)
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
