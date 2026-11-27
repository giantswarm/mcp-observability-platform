package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// responseTypeCode is the response_type of the authorization code flow.
const responseTypeCode = "code"

// dexAuthorizationScopes registers a loopback client at the MCP's
// /oauth/register, sends /oauth/authorize with clientScope, approves the
// consent page, and returns the scopes of the Dex redirect.
func dexAuthorizationScopes(t *testing.T, clientScope string) []string {
	t.Helper()
	settings, err := loadOAuthSettings(slog.New(slog.NewTextHandler(io.Discard, nil)), transportStreamableHTTP)
	require.NoError(t, err)
	oauthHandler, storeClose, err := buildOAuthHandler(settings)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeClose(context.WithoutCancel(t.Context()))) })
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/authorize", oauthHandler.ServeAuthorization)
	mux.HandleFunc("/oauth/register", oauthHandler.ServeClientRegistration)
	mcpHTTP := httptest.NewServer(mux)
	t.Cleanup(mcpHTTP.Close)

	status, registration := (&clientControlsEnv{mcp: mcpHTTP}).register(t, clientControlsRedirectURI)
	require.Equal(t, http.StatusCreated, status, "registration: %v", registration)
	clientID, _ := registration["client_id"].(string)
	require.NotEmpty(t, clientID)

	challenge := sha256.Sum256([]byte(strings.Repeat("v", 64)))
	query := url.Values{
		"response_type":         {responseTypeCode},
		"client_id":             {clientID},
		"redirect_uri":          {clientControlsRedirectURI},
		"state":                 {strings.Repeat("s", 32)},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	if clientScope != "" {
		query.Set("scope", clientScope)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(mcpHTTP.URL + "/oauth/authorize?" + query.Encode())
	require.NoError(t, err)
	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the client gets the consent page first")

	// Approve on the consent page. The consent cookies are Secure, so they
	// are copied by hand onto the plain-HTTP test request.
	consentID := regexp.MustCompile(`name="consent_id" value="([^"]+)"`).FindSubmatch(page)
	require.NotNil(t, consentID, "consent page without consent_id")
	approve, err := http.NewRequest(http.MethodPost, mcpHTTP.URL+"/oauth/authorize",
		strings.NewReader(url.Values{"consent_id": {string(consentID[1])}, "decision": {"approve"}}.Encode()))
	require.NoError(t, err)
	approve.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range resp.Cookies() {
		approve.AddCookie(cookie)
	}
	resp, err = noRedirect.Do(approve)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	location, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, os.Getenv("OAUTH_DEX_ISSUER_URL")+"/auth", location.Scheme+"://"+location.Host+location.Path)
	return strings.Fields(location.Query().Get("scope"))
}

func TestAuthorizeRequestsDexFederatedIDForClientScopes(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		dexScopes   string
		clientScope string
	}{
		{name: "client scope without federated:id", clientScope: "openid email"},
		{name: "no client scope"},
		{name: "OAUTH_DEX_SCOPES with federated:id", dexScopes: "openid,email,federated:id", clientScope: "openid"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fakeDex(t)
			t.Setenv(envOAuthDexScopes, testCase.dexScopes)
			t.Setenv("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION", "true")
			t.Setenv(envOAuthOmitIDToken, "")
			t.Setenv(envOAuthRestrictRegistrationLoopback, "")
			t.Setenv(envOAuthStorageBackend, "")

			require.Contains(t, dexAuthorizationScopes(t, testCase.clientScope), "federated:id")
		})
	}
}
