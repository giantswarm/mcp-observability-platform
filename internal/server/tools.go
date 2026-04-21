// Package server wires the MCP protocol layer for this MCP.
//
// tools.go holds the test-backed helpers used across tool handlers plus the
// explicit empty register* stubs so the server builds and serves /mcp with
// zero tools. Each category of tool (orgs, dashboards, metrics, logs, traces,
// alerts, panels) will live in its own tools_*.go file in PR #10, with its
// own registerXxxTools function wired in from registerTools below.
package server

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// registerTools wires every category of tool into the MCP server. Tool
// definitions themselves live in the corresponding tools_*.go file added in
// PR #10. Empty in this scaffold.
func registerTools(_ *mcpsrv.MCPServer, _ *deps) {}

// registerResources wires MCP resource templates. Empty in this scaffold;
// PR #10 adds the real registrations.
func registerResources(_ *mcpsrv.MCPServer, _ *deps) {}

// registerPrompts wires MCP prompts. Empty in this scaffold; PR #10 adds
// the real registrations.
func registerPrompts(_ *mcpsrv.MCPServer, _ *deps) {}

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

// ---------- argument extraction ----------

func intArg(args map[string]any, name string) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// clampInt clamps n into [lo, hi]. Used for pagination sizes.
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// ---------- pagination helpers for list-of-string results ----------

// paginatedStrings is the JSON projection used by every "list_*" tool that
// returns a flat list of strings (metric names, label values, tag values…).
type paginatedStrings struct {
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	HasMore  bool     `json:"hasMore"`
	Items    []string `json:"items"`
}

// paginateStrings slices values[] into a page. If prefix is non-empty, only
// values whose lowercase form contains the lowercase prefix are kept (applied
// before paging so totals are accurate). Output is always sorted alphabetically.
//
// Callers' input is never mutated: the filter branch allocates a fresh slice,
// and the no-filter branch clones before sorting. This matters because callers
// routinely pass cache-backed slices (resolver org list, CR listings) that
// would otherwise be reordered as a side effect.
func paginateStrings(values []string, prefix string, page, pageSize int) paginatedStrings {
	if prefix != "" {
		lp := strings.ToLower(prefix)
		filtered := make([]string, 0, len(values))
		for _, v := range values {
			if strings.Contains(strings.ToLower(v), lp) {
				filtered = append(filtered, v)
			}
		}
		values = filtered
	} else {
		values = slices.Clone(values)
	}
	sort.Strings(values)
	if pageSize <= 0 {
		pageSize = 100
	}
	pageSize = clampInt(pageSize, 1, 1000)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(values))
	end := min(start+pageSize, len(values))
	return paginatedStrings{
		Total:    len(values),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(values),
		Items:    values[start:end],
	}
}
