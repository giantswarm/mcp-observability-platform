package cmd

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const testOAuthIssuer = "https://mcp.example"
const testExternalIssuer = "https://broker.example"

func setOAuthTestEnvironment(t *testing.T) {
	t.Helper()
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "OAUTH_") {
			t.Setenv(name, "")
		}
	}
	for name, value := range map[string]string{
		"OAUTH_ISSUER":   testOAuthIssuer,
		"OAUTH_PROVIDER": "",
		"OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS": "",
		"OAUTH_TRUSTED_ISSUERS":                     "",
		"OAUTH_DEX_ISSUER_URL":                      "https://dex.example",
		"OAUTH_DEX_CLIENT_ID":                       "observability",
		"OAUTH_DEX_CLIENT_SECRET":                   "test-secret",
	} {
		t.Setenv(name, value)
	}
}

func testOAuthSettings(t *testing.T, transport string) oauthconfig.Settings {
	t.Helper()
	settings, err := loadOAuthSettings(slog.New(slog.DiscardHandler), transport)
	require.NoError(t, err)
	return settings
}

func TestOAuthSettingsDefaults(t *testing.T) {
	for _, transport := range []string{transportStreamableHTTP, transportSSE} {
		t.Run(transport, func(t *testing.T) {
			setOAuthTestEnvironment(t)
			settings := testOAuthSettings(t, transport)
			require.NotNil(t, settings.Dex)
			require.True(t, settings.Server.EnableClientIDMetadataDocuments)
			path := mcpPath
			if transport == transportSSE {
				path = ssePath
			}
			require.Contains(t, settings.Server.ResourceMetadataByPath, path)
		})
	}
}

func TestOAuthTrustedIssuerCompatibility(t *testing.T) {
	for _, entry := range []string{
		`{"issuer":"https://broker.example","jwksURL":"https://keys.example/jwks","allowedAudiences":["observability"],"subjectClaim":"email","allowPrivateIPJWKS":true}`,
		`{"issuer":"https://broker.example","jwksURI":"https://keys.example/jwks","audiences":["observability"],"subjectClaim":"email","allowPrivateIPJWKSHosts":["keys.example"]}`,
	} {
		setOAuthTestEnvironment(t)
		t.Setenv(envOAuthTrustedIssuers, "["+entry+"]")
		settings := testOAuthSettings(t, transportStreamableHTTP)
		require.Len(t, settings.TrustedIssuers, 1)
		issuer := settings.TrustedIssuers[0]
		require.Equal(t, testExternalIssuer, issuer.Issuer)
		require.Equal(t, "https://keys.example/jwks", issuer.JwksURL)
		require.Equal(t, []string{"observability"}, issuer.AllowedAudiences)
		require.Equal(t, []string{"keys.example"}, issuer.AllowPrivateIPJWKSHosts)
		require.Equal(t, "email", issuer.SubjectClaim)
	}
}

func TestOAuthTrustedIssuersFailClosed(t *testing.T) {
	for _, test := range []struct{ name, entry, message string }{
		{"malformed", `{`, "invalid issuer list"},
		{"conflicting JWKS", `[{"jwksURL":"https://one.example","jwksURI":"https://two.example"}]`, "use either"},
		{"conflicting audiences", `[{"allowedAudiences":["one"],"audiences":["two"]}]`, "use either"},
		{"unknown field", `[{"issuer":"https://broker.example","jwksURI":"https://keys.example","audiences":["observability"],"unexpected":true}]`, "unknown field"},
		{"missing audience", `[{"issuer":"https://broker.example","jwksURL":"https://keys.example"}]`, "audiences is required"},
		{"HTTP JWKS", `[{"issuer":"https://broker.example","jwksURL":"http://keys.example","allowedAudiences":["observability"]}]`, "must be an https URL"},
		{"invalid private switch", `[{"allowPrivateIPJWKS":"yes"}]`, "must be a boolean"},
		{"invalid private host", `[{"allowPrivateIPJWKS":true,"jwksURI":"missing-host"}]`, "must name a host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setOAuthTestEnvironment(t)
			t.Setenv(envOAuthTrustedIssuers, test.entry)
			_, err := loadOAuthSettings(slog.New(slog.DiscardHandler), transportStreamableHTTP)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestOAuthSettingsPreservesConfiguredResources(t *testing.T) {
	setOAuthTestEnvironment(t)
	t.Setenv(envOAuthTrustedIssuers, `[{"issuer":"https://broker.example","jwksURI":"https://keys.example/jwks","audiences":["observability"]}]`)
	t.Setenv("OAUTH_AUTHORIZATION_SERVERS", testExternalIssuer)
	t.Setenv("OAUTH_ADDITIONAL_RESOURCES", `[{"resource":"https://mcp.example/mcp","scopesSupported":["read"]}]`)
	settings := testOAuthSettings(t, transportStreamableHTTP)
	require.Equal(t, []string{"read"}, settings.Server.ResourceMetadataByPath[mcpPath].ScopesSupported)
	require.Equal(t, []string{testExternalIssuer}, settings.Server.ResourceMetadataByPath[mcpPath].AuthorizationServers)
	require.Contains(t, settings.Server.ResourceMetadataByPath, "/")
}

func TestOAuthSettingsInheritsAuthorizationServers(t *testing.T) {
	setOAuthTestEnvironment(t)
	t.Setenv("OAUTH_AUTHORIZATION_SERVERS", testOAuthIssuer)
	settings := testOAuthSettings(t, transportSSE)
	require.Equal(t, []string{testOAuthIssuer}, settings.Server.ResourceMetadataByPath[ssePath].AuthorizationServers)
}

func TestOAuthPersistentStorageRequiresEncryption(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		t.Run(strconv.FormatBool(insecure), func(t *testing.T) {
			setOAuthTestEnvironment(t)
			t.Setenv("OAUTH_STORAGE_BACKEND", "valkey")
			t.Setenv("OAUTH_VALKEY_ADDR", "127.0.0.1:1")
			t.Setenv("OAUTH_ALLOW_INSECURE_HTTP", strconv.FormatBool(insecure))
			_, _, err := buildOAuthHandler(testOAuthSettings(t, transportStreamableHTTP))
			require.ErrorContains(t, err, "persistent OAuth token storage requires encryption")
		})
	}
}

func TestOAuthResourceServerModeOnWire(t *testing.T) {
	for _, transport := range []string{transportStreamableHTTP, transportSSE} {
		t.Run(transport, func(t *testing.T) {
			setOAuthTestEnvironment(t)
			t.Setenv("OAUTH_ENABLE_AUTHORIZATION_SERVER", strconv.FormatBool(false))
			t.Setenv("OAUTH_AUTHORIZATION_SERVERS", testExternalIssuer)
			t.Setenv(envOAuthTrustedIssuers, `[{"issuer":"https://broker.example","jwksURI":"https://keys.example/jwks","audiences":["observability"]}]`)
			settings := testOAuthSettings(t, transport)
			require.Nil(t, settings.Dex)
			oauthHandler, cleanup, err := buildOAuthHandler(settings)
			require.NoError(t, err)
			t.Cleanup(func() {
				ctx := context.WithoutCancel(t.Context())
				require.NoError(t, cleanup(ctx))
				require.NoError(t, cleanup(ctx))
			})
			native := mcp.NewServer(&mcp.Implementation{Name: "resource-only", Version: "dev"}, nil)
			host := httptest.NewServer(buildMCPMux(transport, native, oauthHandler))
			t.Cleanup(host.Close)
			path := mcpPath
			if transport == transportSSE {
				path = ssePath
			}
			response, err := host.Client().Get(host.URL + "/.well-known/oauth-protected-resource" + path)
			require.NoError(t, err)
			var metadata struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, testOAuthIssuer+path, metadata.Resource)
			require.Equal(t, []string{testExternalIssuer}, metadata.AuthorizationServers)
			for _, test := range []struct {
				path   string
				status int
			}{{"/oauth/authorize", http.StatusNotFound}, {"/.well-known/oauth-authorization-server", http.StatusNotFound}, {path, http.StatusUnauthorized}} {
				response, err := host.Client().Get(host.URL + test.path)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, test.status, response.StatusCode)
			}
		})
	}
}
