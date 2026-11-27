package middleware

import (
	"context"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
			user, _ := handler.UserInfoFromContext(ctx)
			if user == nil || user.Issuer == "" || user.ID == "" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "authentication required"}}}, nil
			}
			return next(ctx, request)
		}
	}
}
