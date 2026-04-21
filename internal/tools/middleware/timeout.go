package middleware

import (
	"context"
	"time"
)

// WithToolTimeout returns a derived context that enforces a per-tool handler
// deadline. A bounded budget keeps a pathological LogQL query from holding
// the MCP goroutine open until the Grafana HTTP client times out at 30s.
// If ctx already has a deadline the function returns the original ctx and a
// no-op cancel. Helper, not a middleware — tools that want a timeout call
// it around specific downstream operations rather than the whole handler.
func WithToolTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
