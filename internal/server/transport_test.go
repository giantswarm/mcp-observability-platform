package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

const transportIssuer = "https://issuer.example"
const transportSSE = "sse"
const transportAlice = "alice"
const transportAliceEmail = "alice@example.test"

type identityTransport struct {
	base    http.RoundTripper
	subject string
}

func (r identityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("X-Test-Subject", r.subject)
	return r.base.RoundTrip(copy)
}
func attachTestIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		subject := request.Header.Get("X-Test-Subject")
		if subject != "" {
			user := &providers.UserInfo{Issuer: transportIssuer, ID: subject, Email: "private@example.test", EmailVerified: true}
			request = request.WithContext(handler.ContextWithUserInfo(request.Context(), user))
		}
		next.ServeHTTP(writer, request)
	})
}

func TestNativeRequestBodyLimitIncludesChunkedRequests(t *testing.T) {
	native, err := New(goodCfg())
	require.NoError(t, err)
	host := httptest.NewServer(attachTestIdentity(StreamableHTTPHandler(native)))
	t.Cleanup(host.Close)
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"padding":"` + strings.Repeat("x", int(mcp.DefaultMaxRequestBodyBytes)) + `"}}`
	for _, chunked := range []bool{false, true} {
		var reader io.Reader = strings.NewReader(payload)
		if chunked {
			reader = struct{ io.Reader }{reader}
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL, reader)
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2026-07-28")
		request.Header.Set("X-Test-Subject", transportAlice)
		response, err := host.Client().Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
}

func TestNativeTransportsAndAudit(t *testing.T) {
	for _, transport := range []string{"streamable", transportSSE} {
		t.Run(transport, func(t *testing.T) {
			var audit bytes.Buffer
			configuration := goodCfg()
			configuration.Logger = slog.New(slog.NewJSONHandler(&audit, nil))
			configuration.MaxResponseBytes = 128 * 1024
			native, err := New(configuration)
			require.NoError(t, err)
			endpoint := StreamableHTTPHandler(native)
			if transport == transportSSE {
				endpoint = SSEHandler(native)
			}
			host := httptest.NewServer(attachTestIdentity(endpoint))
			defer host.Close()
			httpClient := &http.Client{Transport: identityTransport{base: host.Client().Transport, subject: transportAlice}}
			var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: host.URL, HTTPClient: httpClient, DisableStandaloneSSE: true}
			if transport == transportSSE {
				wire = &mcp.SSEClientTransport{Endpoint: host.URL, HTTPClient: httpClient}
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "native-client", Version: "test"}, nil)
			session, err := client.Connect(t.Context(), wire, nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, session.Close()) }()
			tools, err := session.ListTools(t.Context(), nil)
			require.NoError(t, err)
			require.Greater(t, len(tools.Tools), 30)
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_orgs", Arguments: map[string]any{}})
			require.NoError(t, err)
			require.False(t, result.IsError)
			var record map[string]any
			require.NoError(t, json.Unmarshal(audit.Bytes(), &record))
			require.Equal(t, "list_orgs", record["tool"])
			require.Equal(t, transportIssuer, record["issuer"])
			require.Equal(t, transportAlice, record["subject"])
			require.NotContains(t, audit.String(), "private@example.test")
		})
	}
}

func TestSSESessionCannotChangeCaller(t *testing.T) {
	native, err := New(goodCfg())
	require.NoError(t, err)
	scoped := &scopedSSE{server: native, scopes: make(map[[32]byte]*sseScope)}
	profiles := map[string]providers.UserInfo{
		transportAlice: {Issuer: transportIssuer, ID: transportAlice, Email: transportAliceEmail, EmailVerified: true},
		"subject":      {Issuer: transportIssuer, ID: "bob", Email: transportAliceEmail, EmailVerified: true},
		"issuer":       {Issuer: "https://other.example", ID: transportAlice, Email: transportAliceEmail, EmailVerified: true},
		"email":        {Issuer: transportIssuer, ID: transportAlice, Email: "changed@example.test", EmailVerified: true},
		"unverified":   {Issuer: transportIssuer, ID: transportAlice, Email: transportAliceEmail},
		"delegation":   {Issuer: transportIssuer, ID: transportAlice, Email: transportAliceEmail, EmailVerified: true, ActorIssuer: transportIssuer, ActorSubject: "agent"},
	}
	host := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user := profiles[request.Header.Get("X-Test-Profile")]
		scoped.ServeHTTP(writer, request.WithContext(handler.ContextWithUserInfo(request.Context(), &user)))
	}))
	defer host.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, host.URL+"/sse", nil)
	require.NoError(t, err)
	request.Header.Set("X-Test-Profile", transportAlice)
	stream, err := host.Client().Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, stream.Body.Close()) }()
	require.Equal(t, http.StatusOK, stream.StatusCode)
	scanner := bufio.NewScanner(stream.Body)
	var endpoint string
	for scanner.Scan() {
		if data, found := strings.CutPrefix(scanner.Text(), "data: "); found {
			endpoint = data
			break
		}
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, endpoint)
	if strings.HasPrefix(endpoint, "/") {
		endpoint = host.URL + endpoint
	}
	for _, profile := range []string{"subject", "issuer", "email", "unverified", "delegation", transportAlice} {
		t.Run(profile, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"older-client","version":"test"}}}`))
			require.NoError(t, err)
			request.Header.Set("X-Test-Profile", profile)
			request.Header.Set("Content-Type", "application/json")
			response, err := host.Client().Do(request)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			expected := http.StatusNotFound
			if profile == transportAlice {
				expected = http.StatusAccepted
			}
			require.Equal(t, expected, response.StatusCode)
		})
	}
	require.NoError(t, stream.Body.Close())
	require.Eventually(t, func() bool { scoped.mu.Lock(); defer scoped.mu.Unlock(); return len(scoped.scopes) == 0 }, time.Second, time.Millisecond)
}

func TestStreamableOlderClientsAndOrigin(t *testing.T) {
	native, err := New(goodCfg())
	require.NoError(t, err)
	host := httptest.NewServer(attachTestIdentity(StreamableHTTPHandler(native)))
	defer host.Close()
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + version + `","capabilities":{},"clientInfo":{"name":"older-client","version":"test"}}}`
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL, strings.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("X-Test-Subject", transportAlice)
			response, err := host.Client().Do(request)
			require.NoError(t, err)
			encoded, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode, string(encoded))
			require.Empty(t, response.Header.Get("Mcp-Session-Id"))
			require.Contains(t, string(encoded), version)
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		request, err := http.NewRequestWithContext(t.Context(), method, host.URL, strings.NewReader(`{}`))
		require.NoError(t, err)
		request.Header.Set("Origin", "https://attacker.example")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response, err := host.Client().Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusForbidden, response.StatusCode)
	}
}

type cancellationAuthorizer struct {
	authz.Authorizer
	entered, stopped chan struct{}
}

func (a cancellationAuthorizer) ListOrgs(ctx context.Context) (map[string]authz.Organization, error) {
	close(a.entered)
	<-ctx.Done()
	close(a.stopped)
	return nil, ctx.Err()
}

func TestNativeHTTPPropagatesCallerCancellation(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan struct{})
	configuration := goodCfg()
	configuration.Authorizer = cancellationAuthorizer{entered: entered, stopped: stopped}
	native, err := New(configuration)
	require.NoError(t, err)
	host := httptest.NewServer(attachTestIdentity(StreamableHTTPHandler(native)))
	defer host.Close()
	httpClient := &http.Client{Transport: identityTransport{base: host.Client().Transport, subject: transportAlice}}
	client := mcp.NewClient(&mcp.Implementation{Name: "cancellation-client", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: host.URL, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()
	callContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	complete := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callContext, &mcp.CallToolParams{Name: "list_orgs", Arguments: map[string]any{}})
		complete <- err
	}()
	require.Eventually(t, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-stopped:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		select {
		case err := <-complete:
			require.Error(t, err)
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}
