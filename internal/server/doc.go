// Package server wires the MCP protocol layer: it registers tools and
// resource templates against a mark3labs/mcp-go server. Transport wrapping
// (streamable-HTTP, SSE, stdio) is the caller's concern — this package
// returns the core *mcpsrv.MCPServer plus convenience handlers for the
// HTTP transports.
package server
