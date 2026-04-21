package middleware

import (
	"fmt"
	"os"
	"strconv"
)

// MaxResponseBytes returns the configured cap on tool response body size.
// Set TOOL_MAX_RESPONSE_BYTES to 0 to disable. Default 131072 (128 KiB) —
// enough for most structured responses, small enough that a pathologically
// broad query like `up` on a large cluster returns a useful error instead
// of flooding the LLM context.
func MaxResponseBytes() int {
	if v := os.Getenv("TOOL_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 128 * 1024
}

// ResponseCapError is the structured JSON payload returned when a tool
// response would exceed the configured cap. LLM clients see a typed error
// they can react to (by narrowing the query) rather than a truncated result.
type ResponseCapError struct {
	Error   string `json:"error"` // always "response_too_large"
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// EnforceResponseCap returns a ResponseCapError if body exceeds the
// configured cap, nil otherwise. Callers wrap the error in
// mcp.NewToolResultJSON so the LLM sees a typed payload.
func EnforceResponseCap(body []byte) *ResponseCapError {
	max := MaxResponseBytes()
	if max <= 0 || len(body) <= max {
		return nil
	}
	return &ResponseCapError{
		Error:   "response_too_large",
		Bytes:   len(body),
		Limit:   max,
		Message: fmt.Sprintf("response is %d bytes, exceeds %d byte limit", len(body), max),
		Hint:    "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range",
	}
}
