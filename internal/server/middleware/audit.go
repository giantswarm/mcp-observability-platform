package middleware

import "github.com/mark3labs/mcp-go/mcp"

// auditErrorMessage picks the most useful string to record as the audit
// error field: the handler error first (system_error), otherwise the text
// of an IsError result (user_error). Empty when the call succeeded.
func auditErrorMessage(err error, res *mcp.CallToolResult) string {
	if err != nil {
		return err.Error()
	}
	if res == nil || !res.IsError {
		return ""
	}
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			return t.Text
		}
	}
	return "tool returned isError with no text content"
}
