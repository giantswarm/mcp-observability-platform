package middleware

import (
	"context"
	"testing"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const testIssuer = "https://issuer.example"
const testSubject = "sub-1"

func TestRequireCaller(t *testing.T) {
	for _, user := range []*providers.UserInfo{nil, {}, {Issuer: testIssuer}, {ID: testSubject}, {Issuer: testIssuer, ID: testSubject}, {Issuer: testIssuer, ID: testSubject, ActorSubject: "agent"}} {
		called := false
		ctx := t.Context()
		if user != nil {
			ctx = handler.ContextWithUserInfo(ctx, user)
		}
		result, err := RequireCaller()(func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			called = true
			got, ok := handler.UserInfoFromContext(ctx)
			require.True(t, ok)
			require.Same(t, user, got)
			return &mcp.CallToolResult{}, nil
		})(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "test_tool"}})
		require.NoError(t, err)
		authenticated := user != nil && user.Issuer != "" && user.ID != ""
		require.Equal(t, authenticated, called)
		require.Equal(t, !authenticated, result.IsError)
	}
}

func TestRequireCallerPreservesHandlerError(t *testing.T) {
	ctx := handler.ContextWithUserInfo(t.Context(), &providers.UserInfo{Issuer: testIssuer, ID: testSubject})
	_, err := RequireCaller()(func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, context.DeadlineExceeded
	})(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "test_tool"}})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
