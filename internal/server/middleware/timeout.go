package middleware

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultToolTimeout is applied when TOOL_TIMEOUT isn't set or is malformed.
// Grafana proxy calls over wide PromQL ranges can take 5–15s; 30s leaves
// headroom while guaranteeing a hung upstream doesn't stall the MCP pod.
const DefaultToolTimeout = 30 * time.Second

// ToolTimeout wraps every tool handler with a context deadline. When the
// deadline expires, the handler's ctx.Done() fires; the next(ctx, req) call
// returns an error, and the middleware converts that into an IsError result
// so the LLM sees actionable timeout text rather than a silent hang (mcp-go's
// WithRecovery only catches panics, not hangs).
//
// Runs innermost of the standard middleware chain so Instrument observes the
// timeout as a system_error: span Error, metric outcome=system_error, audit
// record carries the timeout text.
//
// TOOL_TIMEOUT="0s" disables the middleware (ctx is passed through unchanged).
// TOOL_TIMEOUT="10s" / "2m" overrides the default. Env is read per-invocation
// so tests can flip the value between subtests with t.Setenv, matching
// ResponseCap's pattern.
func ToolTimeout() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			timeout := toolTimeout()
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

// toolTimeout returns the configured per-tool-call deadline. "0" / "0s"
// disables the cap; any other invalid value falls back to DefaultToolTimeout.
func toolTimeout() time.Duration {
	v := os.Getenv("TOOL_TIMEOUT")
	if v == "" {
		return DefaultToolTimeout
	}
	if v == "0" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return DefaultToolTimeout
}
