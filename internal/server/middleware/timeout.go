package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultToolTimeout leaves headroom for Grafana proxy calls over wide
// PromQL ranges (typically 5–15s) while guaranteeing a hung upstream
// doesn't stall the MCP pod.
const DefaultToolTimeout = 30 * time.Second

// ToolTimeout wraps every tool handler with a context deadline. On timeout
// the handler's ctx.Done() fires, next returns an error, and the middleware
// converts that into an IsError result so the LLM sees actionable text
// rather than a silent hang.
//
// Runs innermost of the standard middleware chain so Instrument observes
// the timeout as a system_error. timeout <= 0 passes ctx through unchanged.
func ToolTimeout(timeout time.Duration) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if timeout <= 0 {
				return next(ctx, req)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			res, err := next(ctx, req)

			// ctx.Err() == DeadlineExceeded iff OUR timeout fired; a parent
			// cancellation surfaces as context.Canceled and propagates
			// unchanged — the caller already knows they cancelled.
			if err != nil && ctx.Err() == context.DeadlineExceeded {
				return mcp.NewToolResultError(fmt.Sprintf("tool %q exceeded timeout of %s", req.Params.Name, timeout)), nil
			}
			return res, err
		}
	}
}
