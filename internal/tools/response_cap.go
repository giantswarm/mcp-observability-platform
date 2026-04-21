package tools

import (
	"fmt"
	"os"
	"strconv"
)

// maxResponseBytes returns the configured cap on tool response body size.
// Set TOOL_MAX_RESPONSE_BYTES to 0 to disable. Default 131072 (128 KiB) —
// enough for most structured responses, small enough that a pathologically
// broad query like `up` on a large cluster returns a useful error instead of
// flooding the LLM context.
//
// Note: env-read per call is intentional — the cost is sub-microsecond and
// tests need to flip the value via t.Setenv between subtests. Caching via
// sync.OnceValue would break TestEnforceResponseCap_DisabledWithZero.
func maxResponseBytes() int {
	if v := os.Getenv("TOOL_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 128 * 1024
}

// responseCapError is the structured JSON payload returned when a tool
// response would exceed the configured cap. LLM clients see a typed error
// they can react to (by narrowing the query) rather than a truncated result.
type responseCapError struct {
	Error   string `json:"error"` // always "response_too_large"
	Bytes   int    `json:"bytes"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func enforceResponseCap(body []byte) *responseCapError {
	limit := maxResponseBytes()
	if limit <= 0 || len(body) <= limit {
		return nil
	}
	return &responseCapError{
		Error:   "response_too_large",
		Bytes:   len(body),
		Limit:   limit,
		Message: fmt.Sprintf("response is %d bytes, exceeds %d byte limit", len(body), limit),
		Hint:    "narrow the query: add label matchers, aggregate with sum/rate/topk, or shorten the time range",
	}
}
