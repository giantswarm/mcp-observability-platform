package cmd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers/mock"
	oauthserver "github.com/giantswarm/mcp-oauth/server"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPMuxAuthenticationAndResourceMetadata(t *testing.T) {
	for _, transport := range []string{transportStreamableHTTP, transportSSE} {
		t.Run(transport, func(t *testing.T) {
			resourcePath := mcpPath
			if transport == transportSSE {
				resourcePath = ssePath
			}
			store := memory.New()
			logger := slog.New(slog.DiscardHandler)
			oauth, err := oauthserver.New(mock.NewProvider(), store, store, store, &oauthserver.Config{Issuer: "https://mcp.example", ResourceMetadataByPath: map[string]oauthserver.ProtectedResourceConfig{resourcePath: {}}}, logger)
			if err != nil {
				store.Stop()
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, oauth.Shutdown(context.WithoutCancel(t.Context()))) })
			native := mcp.NewServer(&mcp.Implementation{Name: clientControlsName, Version: clientControlsName}, nil)
			host := httptest.NewServer(buildMCPMux(transport, native, handler.New(oauth, logger), false))
			defer host.Close()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+resourcePath, strings.NewReader(`{}`))
			require.NoError(t, err)
			response, err := host.Client().Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
			challenge := response.Header.Get("WWW-Authenticate")
			require.Contains(t, challenge, "Bearer")
			require.Contains(t, challenge, "https://mcp.example/.well-known/oauth-protected-resource"+resourcePath)
			require.NotContains(t, challenge, "invalid_token")
			request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, host.URL+"/.well-known/oauth-protected-resource"+resourcePath, nil)
			require.NoError(t, err)
			response, err = host.Client().Do(request)
			require.NoError(t, err)
			encoded, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode, string(encoded))
			var metadata map[string]any
			require.NoError(t, json.Unmarshal(encoded, &metadata))
			require.Equal(t, "https://mcp.example"+resourcePath, metadata["resource"])
			request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+"/oauth/introspect", nil)
			require.NoError(t, err)
			response, err = host.Client().Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusNotFound, response.StatusCode)
		})
	}
}
