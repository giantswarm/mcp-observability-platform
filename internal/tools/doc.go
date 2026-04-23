// Package tools wires the MCP tool surface of this MCP.
//
// Tool handlers live in per-category files (alerts.go, dashboards.go,
// logs.go, metrics.go, orgs.go, panels.go, silences.go, traces.go). Shared
// helpers live in focused files (datasource.go, pagination.go). The
// package entry point (tools.go) exposes Deps, the pervasive constants,
// and the RegisterAll dispatcher.
package tools
