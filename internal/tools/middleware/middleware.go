// Package middleware composes the cross-cutting concerns every MCP tool
// handler flows through. Each concern is a small `Middleware` =
// `func(Handler) Handler`; `Chain` composes them into a single Handler,
// and `Default` applies the project's standard stack.
//
// Tool registration reads like:
//
//	s.AddTool(
//	    mcp.NewTool("list_orgs", middleware.ReadOnlyAnnotation(), mcp.WithDescription(...)),
//	    middleware.Default("list_orgs", deps)(func(ctx, req) (*mcp.CallToolResult, error) {
//	        // tool body
//	    }),
//	)
//
// Individual middlewares (Tracing, Metrics, …) are exported so tests and
// per-tool tweaks can compose their own stack instead of paying for every
// concern in Default.
package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

// Handler is the mcp-go tool handler signature.
type Handler = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Middleware wraps one tool handler with additional behaviour. Composition
// is right-to-left: `Chain(A, B)(h)` == `A(B(h))` — A runs first, then B,
// then h.
type Middleware func(Handler) Handler

// Deps bundles the handler-scoped dependencies tool registrations need.
// Passed to Default so middleware can reach resolver / grafana / audit
// without each tool threading them through a closure.
type Deps struct {
	Resolver *authz.Resolver
	Grafana  *grafana.Client
}

// Chain composes middlewares into a single middleware. The first argument
// is the outermost — it runs first and calls the next down the chain.
func Chain(mws ...Middleware) Middleware {
	return func(h Handler) Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
}

// Default is the standard middleware stack applied to every tool. Order is
// outermost → innermost: Tracing (so spans cover everything) → Metrics.
// Append new cross-cutting concerns here; tool call sites stay untouched.
func Default(name string, _ *Deps) Middleware {
	return Chain(
		Tracing(name),
		Metrics(name),
	)
}

// Handle applies Default(name, d) to h — the common single-call form at
// tool registration. Equivalent to `Default(name, d)(h)`. Tools that need
// a custom stack can call Chain / Tracing / Metrics / … directly.
func Handle(name string, d *Deps, h Handler) Handler {
	return Default(name, d)(h)
}

// outcome classifies a tool handler's return as "ok" or "err". A non-nil
// error or an IsError result both count as err. Shared by Metrics and (in
// PR 5) Audit so the metric label and audit outcome never drift.
func outcome(res *mcp.CallToolResult, err error) string {
	if err != nil || (res != nil && res.IsError) {
		return "err"
	}
	return "ok"
}
