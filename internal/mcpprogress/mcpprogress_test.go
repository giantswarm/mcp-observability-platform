package mcpprogress

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestReport_NoTokenIsNoop(t *testing.T) {
	// No progressToken on the request → must not panic even without a
	// server in context.
	var req mcp.CallToolRequest
	Report(context.Background(), req, 0.5, 0, "halfway")
}

func TestReport_NoServerInCtxIsNoop(t *testing.T) {
	// Token set but no mcp server in context (e.g. unit-test call site
	// outside the normal request path) → must not panic.
	var req mcp.CallToolRequest
	req.Params.Meta = &mcp.Meta{ProgressToken: "tok-1"}
	Report(context.Background(), req, 0.5, 1, "halfway")
}
