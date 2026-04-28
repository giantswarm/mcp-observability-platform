// Package server constructs the MCP server (tools-only surface),
// composes the tool-handler middleware stack, and provides
// streamable-HTTP / SSE transport wrappers and readiness probes.
// Stdio is the caller's concern — drive it via mcpsrv.ServeStdio
// directly.
package server
