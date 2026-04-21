package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// ReadOnlyAnnotation returns the MCP tool option that flags a tool as
// read-only, open-world, and non-destructive. Every tool in this MCP is
// read-only (no write operations by design) so this is applied uniformly.
// IdempotentHint is intentionally omitted — many tools (query_metrics,
// query_logs, list_alerts) return live data that changes between calls,
// so advertising idempotence across the whole surface would be wrong.
func ReadOnlyAnnotation() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    mcp.ToBoolPtr(true),
		OpenWorldHint:   mcp.ToBoolPtr(true),
		DestructiveHint: mcp.ToBoolPtr(false),
	})
}

// GrafanaOpts packages orgID and caller subject into a RequestOpts so every
// downstream call attributes to the caller via X-Grafana-User.
func GrafanaOpts(ctx context.Context, orgID int64) grafana.RequestOpts {
	return grafana.RequestOpts{OrgID: orgID, Caller: CallerSubject(ctx)}
}
