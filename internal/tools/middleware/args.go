package middleware

import (
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
)

// IntArg reads name from args, coercing common JSON numeric shapes
// (float64, int, int64) and numeric strings to int. Missing or unparseable
// values return 0 — callers validate business-logic constraints.
func IntArg(args map[string]any, name string) int {
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

// StrArg reads name from args as a string. Missing or non-string values
// return "".
func StrArg(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}

// RequireOrg is the canonical arg-extraction error for tools that need an
// "org" argument. Returning an *mcp.CallToolResult lets callers pattern-match
// a single return statement.
func RequireOrg(args map[string]any) (string, *mcp.CallToolResult) {
	org := StrArg(args, "org")
	if org == "" {
		return "", mcp.NewToolResultError("missing required argument 'org'")
	}
	return org, nil
}

// ClampInt clamps n into [lo, hi]. Used for pagination sizes.
func ClampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
