package middleware

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func TestRequireCaller(t *testing.T) {
	for _, caller := range []authz.Caller{{}, {Issuer: testIssuer}, {Subject: testSubject}, {Issuer: testIssuer, Subject: testSubject}, {Issuer: testIssuer, Subject: testSubject, ActorSubject: "actor"}} {
		t.Run(caller.Issuer+caller.Subject+caller.ActorSubject, func(t *testing.T) {
			called := false
			expected := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}
			next := func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				called = true
				require.Equal(t, caller, authz.CallerFromContext(ctx))
				return expected, nil
			}
			result, err := RequireCaller()(next)(authz.WithCaller(t.Context(), caller), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "test_tool"}})
			require.NoError(t, err)
			require.Equal(t, caller.Authenticated(), called)
			if caller.Authenticated() {
				require.Same(t, expected, result)
			} else {
				require.True(t, result.IsError)
			}
		})
	}
}

func TestRequireCallerPreservesHandlerError(t *testing.T) {
	ctx := authz.WithCaller(t.Context(), authz.Caller{Issuer: testIssuer, Subject: testSubject})
	_, err := RequireCaller()(func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, context.DeadlineExceeded
	})(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "test_tool"}})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
