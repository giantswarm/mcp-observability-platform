// Authz-non-bypass integration tests: where the integration tests use
// authztest.Fake to short-circuit RequireOrg, these wire the real
// authz.Authorizer against a fake OrgLister + fake Grafana lookup and
// assert the deny path fires. Catches a future tool that takes an `org`
// argument but skips RequireOrg — RequireCaller alone wouldn't (a caller
// is present, just not authorized for this org).
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// staticOrgLister returns a fixed list of orgs. Implements
// authz.OrgLister — the contract is a single List(ctx) method.
type staticOrgLister struct{ orgs []authz.Organization }

func (r staticOrgLister) List(context.Context) ([]authz.Organization, error) {
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

func (s stubGrafanaForAuthz) LookupUser(_ context.Context, key string) (*grafana.User, error) {
	if id, ok := s.users[key]; ok {
		return &grafana.User{ID: id}, nil
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
		staticOrgLister{orgs: []authz.Organization{{
			Name:        "acme",
			DisplayName: "Acme",
			OrgID:       1,
		}}},
		azClient, nil, 0, 0, -1,
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
	if err := RegisterAll(s, az, gf, ts.URL, "test-token", nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return s, ts.Close
}

// TestHandler_Authz_DeniesUnauthorisedCallerAcrossTools enumerates every
// registered tool that takes an `org` argument and asserts that an
// unauthorised caller receives a structured authz-error result. The
// enumeration is intentional: a future tool added in any path (local
// handler that forgets RequireOrg, bridged tool that bypasses the
// bridge by accident) is caught here without anyone updating a table.
//
// Tools without an `org` argument are skipped. list_orgs is the one
// notable exclusion among org-arg tools: it surfaces what the caller
// CAN see (empty list for unauthorised callers, not an error). Treat
// the list_orgs special case in its own test below.
func TestHandler_Authz_DeniesUnauthorisedCallerAcrossTools(t *testing.T) {
	const caller = "alice@example.com"
	s, closeTS := wireAuthzDenyTest(t, caller)
	defer closeTS()
	ctx := callerCtx(caller)

	tools := s.ListTools()
	// A handful of tools take args beyond `org` that the binder or local
	// handler validates BEFORE running authz; supply minimal stand-in
	// values so the call reaches the authz boundary. Unknown args are
	// either ignored (bridged) or used by validation we don't care about
	// in this test (local).
	stockArgs := map[string]any{
		"org":            "acme",
		"uid":            "abc",
		"query":          "up",
		"name":           "x",
		"label":          "x",
		"tag":            "service.name",
		"metric":         "http_requests_total",
		"promql":         "up",
		"service":        "api",
		"fingerprint":    "0123456789abcdef",
		"dashboardUid":   "abc",
		"panelId":        1,
		"datasource_uid": "any", // alerting_manage_rules: bridge clobbers, but its own validate() expects an operation
		"operation":      "list",
		"q":              "{}",
		"logql":          `{job="x"}`,
		"queryType":      "instant",
	}

	denyCount := 0
	for name, st := range tools {
		if name == "list_orgs" {
			continue // tested separately
		}
		if !slices.Contains(st.Tool.InputSchema.Required, "org") {
			continue
		}
		denyCount++
		t.Run(name, func(t *testing.T) {
			res := callToolWithCtx(t, ctx, s, name, stockArgs)
			if !res.IsError {
				t.Fatalf("tool %q must return IsError for unauthorised caller; got %s", name, resultText(res))
			}
			text := resultText(res)
			if !strings.Contains(text, "not authorised") {
				t.Errorf("tool %q error should name authz; got %q", name, text)
			}
		})
	}
	// Defensive: enumerator must actually find tools. A registration
	// regression that drops `org` from every tool's schema would
	// silently let this test pass with zero cases otherwise.
	if denyCount < 5 {
		t.Errorf("enumerated only %d org-arg tools; expected >= 5 (registration regression?)", denyCount)
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
