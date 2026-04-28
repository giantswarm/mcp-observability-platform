package middleware

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultMaxResponseBytes fits typical structured responses but trips
// pathologically broad queries before they flood the LLM context.
const DefaultMaxResponseBytes = 128 * 1024

// ResponseCap swaps oversized TextContent for a typed response_too_large
// payload and sets IsError so the LLM sees an actionable failure instead
// of garbled text. limit <= 0 disables capping.
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

type responseCapError struct {
	Error   string `json:"error"`
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}
