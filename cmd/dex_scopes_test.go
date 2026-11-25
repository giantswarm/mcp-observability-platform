package cmd

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers"
)

// fakeDex serves the OIDC discovery document that dex.NewProvider fetches,
// and sets the OAUTH_DEX_* env vars to point at it.
func fakeDex(t *testing.T) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		issuer := "https://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/auth",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/keys",
			"userinfo_endpoint":      issuer + "/userinfo",
		})
	}))
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}

	t.Setenv("OAUTH_DEX_ISSUER_URL", srv.URL)
	t.Setenv("OAUTH_DEX_CLIENT_ID", "mcp-observability-platform")
	t.Setenv("OAUTH_DEX_CLIENT_SECRET", "secret")
	t.Setenv("OAUTH_DEX_CONNECTOR_ID", "giantswarm-github")
	t.Setenv("OAUTH_DEX_CA_FILE", caFile)
	t.Setenv("OAUTH_ISSUER", "https://mcp.example.com")
	t.Setenv("OAUTH_DEX_REDIRECT_URL", "")
	t.Setenv("OAUTH_DEX_AUDIENCES", "")
	t.Setenv(envOAuthDexScopes, "")
}

// authorizationQuery returns the query of the provider's authorization URL
// for a request without client scopes.
func authorizationQuery(t *testing.T, p providers.Provider) url.Values {
	t.Helper()
	u, err := url.Parse(p.AuthorizationURL("state", "challenge", "S256", nil, nil))
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	return u.Query()
}

func TestDexProviderFromEnvRequestsFederatedID(t *testing.T) {
	fakeDex(t)

	got, err := dexProviderFromEnv()
	if err != nil {
		t.Fatalf("dexProviderFromEnv: %v", err)
	}
	const want = "openid profile email groups offline_access federated:id"
	if scopes := got.DefaultScopes(); !slices.Equal(scopes, strings.Fields(want)) {
		t.Errorf("DefaultScopes = %v, want %v", scopes, want)
	}
	query := authorizationQuery(t, got)
	if scope := query.Get("scope"); scope != want {
		t.Errorf("authorization scope = %q, want %q", scope, want)
	}

	// Every other setting matches the provider of DexFromEnv.
	upstream, err := oauthconfig.DexFromEnv()
	if err != nil {
		t.Fatalf("DexFromEnv: %v", err)
	}
	upstreamQuery := authorizationQuery(t, upstream)
	query.Del("scope")
	upstreamQuery.Del("scope")
	if query.Encode() != upstreamQuery.Encode() {
		t.Errorf("authorization query = %q, want %q", query.Encode(), upstreamQuery.Encode())
	}
	if query.Get("connector_id") != "giantswarm-github" || query.Get("redirect_uri") != "https://mcp.example.com/oauth/callback" {
		t.Errorf("authorization query = %q, want the connector and default redirect of DexFromEnv", query.Encode())
	}
}

func TestDexProviderFromEnvKeepsOAuthDexScopes(t *testing.T) {
	fakeDex(t)
	t.Setenv(envOAuthDexScopes, "openid,email")

	got, err := dexProviderFromEnv()
	if err != nil {
		t.Fatalf("dexProviderFromEnv: %v", err)
	}
	if scopes := got.DefaultScopes(); !slices.Equal(scopes, strings.Split("openid,email", ",")) {
		t.Errorf("DefaultScopes = %v, want [openid email]", scopes)
	}
}
