package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers/mock"
	"github.com/giantswarm/mcp-oauth/storage"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
	"github.com/giantswarm/mcp-observability-platform/internal/server"
)

const (
	clientControlsJWTHeader   = "X-JWT-Assertion"
	clientControlsDexIDToken  = "dex-id-token-of-the-caller" //nolint:gosec // test value
	clientControlsRedirectURI = "http://127.0.0.1:43210/callback"
	clientControlsUserID      = "client-controls-user"
)

// fakeGrafana records the requests the MCP sends to Grafana and answers
// /api/user/orgs with one Viewer membership in org 1.
type fakeGrafana struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (f *fakeGrafana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.mu.Unlock()
	if r.URL.Path != "/api/user/orgs" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`[{"orgId":1,"name":"acme","role":"Viewer"}]`))
}

func (f *fakeGrafana) recorded() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

type clientControlsOrgLister struct{}

func (clientControlsOrgLister) List(context.Context) ([]authz.Organization, error) {
	return []authz.Organization{{Name: "acme", DisplayName: "Acme", OrgID: 1}}, nil
}

// clientControlsEnv wires the MCP's HTTP surface the way runServe does in
// GRAFANA_AUTH_MODE=jwt: the mcp-oauth handler built from the MCP's
// OAuth config, a mock Dex provider, idTokenResolver and a fake Grafana.
type clientControlsEnv struct {
	mcp     *httptest.Server
	grafana *fakeGrafana
	store   *memory.Store
}

func newClientControlsEnv(t *testing.T) *clientControlsEnv {
	t.Helper()
	t.Setenv("OAUTH_ISSUER", "https://mcp.example.com")
	t.Setenv("OAUTH_ALLOW_PUBLIC_CLIENT_REGISTRATION", "true")
	t.Setenv(envOAuthOmitIDToken, "")
	t.Setenv(envOAuthRestrictRegistrationLoopback, "")

	cfg, err := oauthConfigFromEnv(true)
	require.NoError(t, err)
	// The test redirect URIs must not depend on DNS.
	cfg.DisableDNSValidation = true

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := memory.New()
	t.Cleanup(store.Stop)
	srv, err := oauth.NewServerWithCombined(mock.NewProvider(), store, cfg, logger)
	require.NoError(t, err)
	oauthHandler := handler.New(srv, logger)

	fake := &fakeGrafana{}
	grafanaServer := httptest.NewServer(fake)
	t.Cleanup(grafanaServer.Close)
	grafanaClient, err := grafana.New(grafana.Config{URL: grafanaServer.URL, JWTHeader: clientControlsJWTHeader})
	require.NoError(t, err)
	authorizer := authz.NewAuthorizer(clientControlsOrgLister{}, grafanaClient,
		authz.DefaultCacheTTL, authz.DefaultNegativeCacheTTL, authz.WithCallerToken())
	mcpServer, err := server.New(server.Config{
		Logger:           logger,
		Authorizer:       authorizer,
		Grafana:          grafanaClient,
		GrafanaURL:       grafanaServer.URL,
		GrafanaJWTHeader: clientControlsJWTHeader,
	})
	require.NoError(t, err)

	mux := buildMCPMux(transportStreamableHTTP, mcpServer, oauthHandler, idTokenResolver(srv.TokenStore()))
	mcpHTTP := httptest.NewServer(mux)
	t.Cleanup(mcpHTTP.Close)
	return &clientControlsEnv{mcp: mcpHTTP, grafana: fake, store: store}
}

// register posts a public client registration with redirectURI and
// returns the response status and decoded body.
func (e *clientControlsEnv) register(t *testing.T, redirectURI string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"client_name":                "client-controls-test",
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	require.NoError(t, err)
	resp, err := http.Post(e.mcp.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// signIn registers a loopback client, stores the Dex token of
// clientControlsUserID the way the provider callback does, and redeems an
// authorization code at /oauth/token. It returns the decoded token
// response.
func (e *clientControlsEnv) signIn(t *testing.T, dexToken *oauth2.Token) map[string]any {
	t.Helper()
	ctx := context.Background()
	status, registration := e.register(t, clientControlsRedirectURI)
	require.Equal(t, http.StatusCreated, status, "registration: %v", registration)
	clientID, _ := registration["client_id"].(string)
	require.NotEmpty(t, clientID)

	require.NoError(t, e.store.SaveUserProviderToken(ctx, clientControlsUserID, dexToken))
	verifier := strings.Repeat("v", 64)
	challenge := sha256.Sum256([]byte(verifier))
	code := &storage.AuthorizationCode{
		Code:                "client-controls-code",
		ClientID:            clientID,
		RedirectURI:         clientControlsRedirectURI,
		Scope:               "openid",
		CodeChallenge:       base64.RawURLEncoding.EncodeToString(challenge[:]),
		CodeChallengeMethod: "S256",
		UserID:              clientControlsUserID,
		ProviderToken:       dexToken,
		CreatedAt:           time.Now(),
		ExpiresAt:           time.Now().Add(10 * time.Minute),
	}
	require.NoError(t, e.store.SaveAuthorizationCode(ctx, code))

	resp, err := http.PostForm(e.mcp.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code.Code},
		"redirect_uri":  {clientControlsRedirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var tokenResponse map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResponse))
	require.Equal(t, http.StatusOK, resp.StatusCode, "token response: %v", tokenResponse)
	return tokenResponse
}

// listOrgs calls the list_orgs tool at /mcp with accessToken as the
// bearer.
func (e *clientControlsEnv) listOrgs(t *testing.T, accessToken string) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	client, err := mcpclient.NewStreamableHttpClient(e.mcp.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + accessToken}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Start(ctx))
	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initialize.Params.ClientInfo = mcp.Implementation{Name: "client-controls-test", Version: "0"}
	_, err = client.Initialize(ctx, initialize)
	require.NoError(t, err)
	call := mcp.CallToolRequest{}
	call.Params.Name = "list_orgs"
	result, err := client.CallTool(ctx, call)
	require.NoError(t, err)
	return result
}

func dexTokenWith(extra map[string]any) *oauth2.Token {
	token := &oauth2.Token{ //nolint:gosec // G101: test values
		AccessToken:  "dex-access-token",
		RefreshToken: "dex-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	if extra != nil {
		token = token.WithExtra(extra)
	}
	return token
}

// TestClientControls_TokenResponseOmitsIDTokenAndGrafanaGetsIt checks that
// the token response carries no Dex id_token, and that a tool call with
// the issued token still forwards the Dex id_token to Grafana in the JWT
// header, with no Authorization header.
func TestClientControls_TokenResponseOmitsIDTokenAndGrafanaGetsIt(t *testing.T) {
	env := newClientControlsEnv(t)
	tokenResponse := env.signIn(t, dexTokenWith(map[string]any{"id_token": clientControlsDexIDToken}))

	require.NotContains(t, tokenResponse, "id_token")
	accessToken, _ := tokenResponse["access_token"].(string)
	require.NotEmpty(t, accessToken)

	result := env.listOrgs(t, accessToken)
	require.False(t, result.IsError, "list_orgs: %+v", result.Content)

	requests := env.grafana.recorded()
	require.NotEmpty(t, requests, "list_orgs must call Grafana")
	for _, r := range requests {
		require.Equal(t, clientControlsDexIDToken, r.Header.Get(clientControlsJWTHeader), "%s %s", r.Method, r.URL.Path)
		require.Empty(t, r.Header.Get("Authorization"), "%s %s", r.Method, r.URL.Path)
	}
}

// TestClientControls_CallerWithoutDexTokenIsRefusedBeforeGrafana checks
// that a caller whose stored provider token has no Dex id_token is refused
// without any Grafana call.
func TestClientControls_CallerWithoutDexTokenIsRefusedBeforeGrafana(t *testing.T) {
	env := newClientControlsEnv(t)
	tokenResponse := env.signIn(t, dexTokenWith(nil))
	accessToken, _ := tokenResponse["access_token"].(string)
	require.NotEmpty(t, accessToken)

	result := env.listOrgs(t, accessToken)
	require.True(t, result.IsError, "list_orgs must fail without a Dex token")
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "content: %+v", result.Content[0])
	require.Contains(t, text.Text, "cannot be forwarded to Grafana")
	require.Empty(t, env.grafana.recorded(), "no Grafana call without a Dex token")
}

// TestClientControls_RegistrationAcceptsOnlyLoopbackRedirectURIs checks
// that /oauth/register accepts a loopback redirect URI and refuses any
// other one with invalid_redirect_uri.
func TestClientControls_RegistrationAcceptsOnlyLoopbackRedirectURIs(t *testing.T) {
	for _, testCase := range []struct {
		name, redirectURI string
		wantStatus        int
	}{
		{name: "loopback IPv4", redirectURI: "http://127.0.0.1:43210/callback", wantStatus: http.StatusCreated},
		{name: "loopback IPv6", redirectURI: "http://[::1]:43210/callback", wantStatus: http.StatusCreated},
		{name: "localhost", redirectURI: "http://localhost:43210/callback", wantStatus: http.StatusCreated},
		{name: "https web host", redirectURI: "https://app.example.com/callback", wantStatus: http.StatusBadRequest},
		{name: "custom scheme", redirectURI: "cursor://anysphere.cursor-retrieval/oauth/callback", wantStatus: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := newClientControlsEnv(t)
			status, body := env.register(t, testCase.redirectURI)
			require.Equal(t, testCase.wantStatus, status, "body: %v", body)
			if testCase.wantStatus == http.StatusBadRequest {
				require.Equal(t, "invalid_redirect_uri", body["error"])
			}
		})
	}
}
