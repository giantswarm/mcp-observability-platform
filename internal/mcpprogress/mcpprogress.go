// Package mcpprogress sends `notifications/progress` to MCP clients during
// long-running tool calls. Reporting is idempotent: when the client did not
// attach a progressToken to the request, Report is a no-op. Tool handlers
// call it at natural milestones (before a slow downstream request, after
// pagination chunks) without caring whether the client is listening.
//
// Cancellation is handled by mcp-go directly — when the client sends
// `notifications/cancelled` the tool handler's ctx is cancelled; handlers
// only need to pass ctx through to downstream calls, which the existing
// Grafana client already does.
package mcpprogress

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// Report emits a progress notification to the client if the request carried
// a progressToken. Total is optional — pass 0 to signal "unknown length".
// Errors are swallowed on purpose: a progress notification failing must not
// abort the tool call.
func Report(ctx context.Context, req mcp.CallToolRequest, progress, total float64, message string) {
	if req.Params.Meta == nil || req.Params.Meta.ProgressToken == nil {
		return
	}
	token := req.Params.Meta.ProgressToken
	srv := mcpsrv.ServerFromContext(ctx)
	if srv == nil {
		return
	}
	payload := map[string]any{
		"progressToken": token,
		"progress":      progress,
	}
	if total > 0 {
		payload["total"] = total
	}
	if message != "" {
		payload["message"] = message
	}
	_ = srv.SendNotificationToClient(ctx, "notifications/progress", payload)
}
