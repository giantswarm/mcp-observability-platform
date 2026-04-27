// Authz-non-bypass integration tests: where the existing handler tests use
// a fakeAuthz that always grants, these wire the real authz.Authorizer
// against a fake OrgRegistry + fake Grafana lookup and assert the deny
// path fires. Belt-and-braces for the RequireCaller middleware: catches
// a future tool that takes an `org` argument but skips RequireOrg, where
// RequireCaller alone wouldn't (a caller is present, just not authorized).
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// staticOrgRegistry returns a fixed list of orgs. Implements
// authz.OrgRegistry — the contract is a single List(ctx) method.
type staticOrgRegistry struct{ orgs []authz.Organization }

func (r staticOrgRegistry) List(context.Context) ([]authz.Organization, error) {
	return r.orgs, nil
}

// stubGrafanaForAuthz is a grafana.Client stub that supports only the
// methods authz.Authorizer.load actually calls (LookupUser, UserOrgs)
// plus a no-op for the tool-handler downstream methods so calls past
// the authz boundary fail loudly via the test server (which we set to
// fail the test on any request).
type stubGrafanaForAuthz struct {
	grafana.Client // embedded interface — unused methods nil-panic
	users          map[string]int64
	memberships    map[int64][]grafana.UserOrgMembership
}

type lookupUser = struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Login string `json:"login"`
}

func (s stubGrafanaForAuthz) LookupUser(_ context.Context, key string) (*lookupUser, error) {
	if id, ok := s.users[key]; ok {
		return &lookupUser{ID: id, Email: key}, nil
	}
	return nil, nil
}

func (s stubGrafanaForAuthz) UserOrgs(_ context.Context, id int64) ([]grafana.UserOrgMembership, error) {
	return s.memberships[id], nil
}

// callerCtx puts a UserInfo on ctx via the same path PromoteOAuthCaller
// uses at the HTTP boundary, so the real Authorizer can derive the
// caller from CallerFromContext.
func callerCtx(email string) context.Context {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = r.WithContext(oauth.ContextWithUserInfo(r.Context(),
		&providers.UserInfo{ID: "sub-" + email, Email: email}))
	return authz.PromoteOAuthCaller(context.Background(), r)
}

// wireAuthzDenyTest is the non-bypass equivalent of wireHandlerTest: the
// real Authorizer is constructed against a fake registry and a fake
// Grafana lookup that returns a user with NO org memberships. Every tool
// call with org="acme" must therefore fire ErrNotAuthorised. The
// downstream Grafana httptest.Server is wired to fail the test on any
// request, proving authz fires before any tool-side proxy call.
func wireAuthzDenyTest(t *testing.T, callerEmail string) (*mcpsrv.MCPServer, func()) {
	t.Helper()
	// Authorizer client (LookupUser + UserOrgs).
	azClient := stubGrafanaForAuthz{
		users:       map[string]int64{callerEmail: 1},
		memberships: map[int64][]grafana.UserOrgMembership{1: {}}, // user exists, zero orgs
	}
	az, err := authz.NewAuthorizer(
		staticOrgRegistry{orgs: []authz.Organization{{
			Name:        "acme",
			DisplayName: "Acme",
			OrgID:       1,
		}}},
		azClient, nil, 0, 0, -1, // -1 disables the LRU so each call hits the upstream stubs.
	)
	if err != nil {
		t.Fatalf("authz.NewAuthorizer: %v", err)
	}

	// Tool-side Grafana client. The test fails if the tool reaches here —
	// authz must deny first.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("downstream Grafana should not be called when authz denies; got %s %s", r.Method, r.URL.Path)
		http.Error(w, "test failure", http.StatusInternalServerError)
	}))
	gf, err := grafana.New(grafana.Config{URL: ts.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("grafana.New: %v", err)
	}

	s := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false))
	RegisterAll(s, az, gf)
	return s, ts.Close
}

// TestHandler_Authz_DeniesUnauthorisedCallerAcrossTools picks one tool
// from each major category and confirms a caller with no org membership
// gets a structured authz-error result rather than a happy-path response
// or a panic. If a future tool category is added that forgets RequireOrg
// (or its equivalent), this test catches it.
func TestHandler_Authz_DeniesUnauthorisedCallerAcrossTools(t *testing.T) {
	const caller = "alice@example.com"
	s, closeTS := wireAuthzDenyTest(t, caller)
	defer closeTS()
	ctx := callerCtx(caller)

	// One representative tool per category that takes `org`. list_orgs is
	// excluded — it lists across all orgs the caller can see, which is
	// supposed to return an empty result for unauthorized callers, not an
	// error. (Tested separately below.)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"list_datasources", map[string]any{"org": "acme"}},
		{"get_dashboard_by_uid", map[string]any{"org": "acme", "uid": "abc"}},
		{"query_prometheus", map[string]any{"org": "acme", "query": "up"}},
		{"query_loki_logs", map[string]any{"org": "acme", "query": `{job="x"}`}},
		{"list_alerts", map[string]any{"org": "acme"}},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			res := callToolWithCtx(t, ctx, s, c.tool, c.args)
			if !res.IsError {
				t.Fatalf("tool %q must return IsError for unauthorised caller; got %s", c.tool, resultText(res))
			}
			text := resultText(res)
			if !strings.Contains(text, "not authorised") {
				t.Errorf("tool %q error should name authz; got %q", c.tool, text)
			}
		})
	}
}

// TestHandler_Authz_ListOrgsReturnsEmptyForUnauthorisedCaller covers the
// list_orgs special case: it surfaces what the caller CAN see, which is
// the empty list when they have no memberships. Different shape from the
// deny-path test above, same authz boundary.
func TestHandler_Authz_ListOrgsReturnsEmptyForUnauthorisedCaller(t *testing.T) {
	const caller = "alice@example.com"
	s, closeTS := wireAuthzDenyTest(t, caller)
	defer closeTS()
	ctx := callerCtx(caller)

	res := callToolWithCtx(t, ctx, s, "list_orgs", nil)
	if res.IsError {
		t.Fatalf("list_orgs should return empty access, not IsError: %s", resultText(res))
	}
	var got struct {
		Orgs []json.RawMessage `json:"orgs"`
	}
	if err := json.Unmarshal([]byte(resultText(res)), &got); err != nil {
		t.Fatalf("decode list_orgs response: %v (raw: %s)", err, resultText(res))
	}
	if len(got.Orgs) != 0 {
		t.Errorf("expected empty orgs list, got %d entries", len(got.Orgs))
	}
}

// TestHandler_Authz_RejectsMissingOrgArgument: tools must reject calls
// missing the `org` argument with a useful error. RequireString returns
// the error before authz runs, so this is purely arg validation — but
// it's part of the same boundary contract.
func TestHandler_Authz_RejectsMissingOrgArgument(t *testing.T) {
	const caller = "alice@example.com"
	s, closeTS := wireAuthzDenyTest(t, caller)
	defer closeTS()
	ctx := callerCtx(caller)

	res := callToolWithCtx(t, ctx, s, "list_datasources", map[string]any{})
	if !res.IsError {
		t.Fatalf("missing-org should return IsError; got %s", resultText(res))
	}
	if !strings.Contains(strings.ToLower(resultText(res)), "org") {
		t.Errorf("error should name the missing arg; got %q", resultText(res))
	}
}

