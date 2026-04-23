package middleware

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultMaxResponseBytes is enough for typical structured responses; small
// enough that a pathologically broad query returns a useful error instead
// of flooding the LLM context.
const DefaultMaxResponseBytes = 128 * 1024

// ResponseCap replaces oversized TextContent in the tool result with a
// structured response_too_large payload, marking the result IsError so
// Classify routes it to user_error. limit <= 0 disables capping.
func ResponseCap(limit int) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := next(ctx, req)
			if err != nil || res == nil {
				return res, err
			}
			if limit <= 0 {
				return res, err
			}
			for i, c := range res.Content {
				t, ok := c.(mcp.TextContent)
				if !ok || len(t.Text) <= limit {
					continue
				}
				// Replace oversized content with the structured cap payload
				// and mark the result as IsError so Classify() buckets this
				// as user_error (expected, LLM-actionable — not a server bug).
				payload, _ := json.Marshal(responseCapError{
					Error:   "response_too_large",
					Bytes:   len(t.Text),
					Limit:   limit,
					Message: fmt.Sprintf("response is %d bytes, exceeds %d byte limit", len(t.Text), limit),
					Hint:    "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range",
				})
				res.Content[i] = mcp.TextContent{Type: "text", Text: string(payload)}
				res.IsError = true
			}
			return res, err
		}
	}
}

// responseCapError is the structured JSON payload returned when a tool
// response exceeds the configured cap. LLM clients see a typed error
// they can react to (by narrowing the query) rather than a truncated
// result.
type responseCapError struct {
	Error   string `json:"error"` // always "response_too_large"
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}
