package grafana

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestServer returns an httptest.Server whose handler records the
// received request and replies with a caller-provided body/status.
func newTestServer(handler http.HandlerFunc) (*httptest.Server, *Client) {
	ts := httptest.NewServer(handler)
	c, _ := New(Config{URL: ts.URL, Token: "test-token"})
	return ts, c
}

func TestClient_AuthHeader_Bearer(t *testing.T) {
	var gotAuth string
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("{}"))
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
		w.Write([]byte("{}"))
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
		w.Write([]byte("{}"))
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
		w.Write([]byte("[]"))
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
		w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid query"}`))
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
		w.Write([]byte("{}"))
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
		w.Write([]byte("<html>Rendering plugin is not installed</html>"))
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
		w.Write([]byte{0x89, 'P', 'N', 'G'})
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

// Satisfy a linter complaint about unused imports if the test file is edited.
var _ = io.EOF
