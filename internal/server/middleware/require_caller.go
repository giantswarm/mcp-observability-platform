package middleware

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// RequireCaller is the fail-closed gate that catches any future tool
// added without explicit authz code. Real authentication runs in
// mcp-oauth's ValidateToken at the HTTP edge; per-org/role checks stay
// in handlers (they need the org arg). Returns an IsError result, not
// a Go error, to match the rest of the tool surface. Stdio sessions
// trip this guard unless they install a caller via a context func.
func RequireCaller() func(mcp.ToolHandler) mcp.ToolHandler {
	return func(next mcp.ToolHandler) mcp.ToolHandler {
		return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !authz.CallerFromContext(ctx).Authenticated() {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "authentication required"}}}, nil
			}
			return next(ctx, request)
		}
	}
}
