package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

func callRequireCaller(t *testing.T, ctx context.Context, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	t.Helper()
	wrapped := RequireCaller()(handler)
	req := mcp.CallToolRequest{}
	req.Params.Name = "test_tool"
	return wrapped(ctx, req)
}

// ctxWithCaller mirrors what PromoteOAuthCaller does at the HTTP boundary:
// take an mcp-oauth UserInfo and lift it onto the MCP-level handler context.
// Tests use this to simulate an authenticated tool call.
func ctxWithCaller(ui *providers.UserInfo) context.Context {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = r.WithContext(oauth.ContextWithUserInfo(r.Context(), ui))
	return authz.PromoteOAuthCaller(context.Background(), r)
}

func TestRequireCaller_RejectsEmptyContext(t *testing.T) {
	var handlerRan bool
	res, err := callRequireCaller(t, context.Background(), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		handlerRan = true
		return mcp.NewToolResultText("should not run"), nil
	})
	if err != nil {
		t.Fatalf("middleware must convert missing-caller into IsError result, not propagate err; got %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError=true result; got %+v", res)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "authentication required") {
		t.Errorf("rejection text missing expected phrase: %q", text)
	}
	if handlerRan {
		t.Errorf("handler must not run when caller is missing")
	}
}

func TestRequireCaller_RejectsCallerWithEmptyFields(t *testing.T) {
	// UserInfo present in context but neither Email nor ID set — the
	// authz.Caller.Authenticated() check must treat this as "no identity" and the
	// middleware must reject. A token validated against the IdP but
	// missing identifying claims is not the same as "authenticated."
	ctx := ctxWithCaller(&providers.UserInfo{})
	res, err := callRequireCaller(t, ctx, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("should not run"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("empty-fields UserInfo must be rejected; got %+v", res)
	}
}

func TestRequireCaller_PassesThroughAuthenticatedCaller(t *testing.T) {
	ctx := ctxWithCaller(&providers.UserInfo{ID: "sub-1", Email: "alice@example.com"})
	res, err := callRequireCaller(t, ctx, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The handler must see the same caller the middleware checked.
		if got := authz.CallerFromContext(ctx).Subject; got != "sub-1" {
			t.Errorf("handler saw Subject=%q, want sub-1", got)
		}
		return mcp.NewToolResultText("ok"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Errorf("authenticated caller marked IsError")
	}
	if res.Content[0].(mcp.TextContent).Text != "ok" {
		t.Errorf("content mutated")
	}
}

func TestRequireCaller_HandlerErrorPassesThrough(t *testing.T) {
	// When the handler does run (caller present), errors from it propagate
	// unchanged — the middleware doesn't swallow downstream failures.
	ctx := ctxWithCaller(&providers.UserInfo{ID: "sub-1"})
	wantErr := context.DeadlineExceeded
	_, err := callRequireCaller(t, ctx, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, wantErr
	})
	if err != wantErr {
		t.Errorf("handler error masked; got %v, want %v", err, wantErr)
	}
}
