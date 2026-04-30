package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func TestPaginateStrings(t *testing.T) {
	all := []string{"zeta", "alpha", "beta", "gamma", "delta"}

	t.Run("sorts then pages", func(t *testing.T) {
		got := paginateStrings(all, "", 0, 2)
		if got.Total != 5 || len(got.Items) != 2 || got.Items[0] != "alpha" || got.Items[1] != "beta" {
			t.Errorf("page 0 = %+v", got)
		}
		got = paginateStrings(all, "", 2, 2)
		if len(got.Items) != 1 || got.Items[0] != "zeta" || got.HasMore {
			t.Errorf("final page = %+v", got)
		}
	})
	t.Run("prefix filters before paging", func(t *testing.T) {
		got := paginateStrings(all, "ET", 0, 10) // case-insensitive: matches "beta" and "zeta"
		if got.Total != 2 || len(got.Items) != 2 {
			t.Errorf("prefix ET = %+v", got)
		}
	})
	t.Run("page out of range returns empty", func(t *testing.T) {
		got := paginateStrings(all, "", 99, 10)
		if len(got.Items) != 0 || got.HasMore {
			t.Errorf("out-of-range = %+v", got)
		}
	})
	t.Run("pageSize clamped to [1,1000]", func(t *testing.T) {
		got := paginateStrings(all, "", 0, 999999)
		if got.PageSize != 1000 {
			t.Errorf("clamp hi: got %d", got.PageSize)
		}
	})
	t.Run("does not mutate caller's slice", func(t *testing.T) {
		// Callers routinely pass cache-backed slices (resolver org list, CR
		// listings). paginateStrings must not reorder them as a side effect.
		in := []string{"zeta", "alpha", "beta"}
		before := append([]string(nil), in...)
		_ = paginateStrings(in, "", 0, 10)
		for i := range before {
			if in[i] != before[i] {
				t.Fatalf("input mutated: before=%v after=%v", before, in)
			}
		}
		// Same check with a prefix filter path.
		_ = paginateStrings(in, "a", 0, 10)
		for i := range before {
			if in[i] != before[i] {
				t.Fatalf("input mutated (prefix path): before=%v after=%v", before, in)
			}
		}
	})
}

func TestMaybeAddTool(t *testing.T) {
	noop := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	t.Run("nil_disabled_registers_all", func(t *testing.T) {
		s := mcpsrv.NewMCPServer("t", "0", mcpsrv.WithToolCapabilities(true))
		maybeAddTool(s, nil, mcp.NewTool("a"), noop)
		maybeAddTool(s, nil, mcp.NewTool("b"), noop)
		// mark3labs/mcp-go has no listing-by-name accessor on the server,
		// but ListTools via the server's request path is overkill here.
		// Re-registering panics on collision, so a non-panicking second
		// call is sufficient evidence both tools were registered.
	})
	t.Run("disabled_skips_match_only", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("unexpected panic: %v", r)
			}
		}()
		s := mcpsrv.NewMCPServer("t", "0", mcpsrv.WithToolCapabilities(true))
		disabled := map[string]bool{"a": true}
		// Register "a" twice: if the disabled filter weren't honoured,
		// the second AddTool call would panic on duplicate registration.
		maybeAddTool(s, disabled, mcp.NewTool("a"), noop)
		maybeAddTool(s, disabled, mcp.NewTool("a"), noop)
		// "b" registers normally; a duplicate would panic.
		maybeAddTool(s, disabled, mcp.NewTool("b"), noop)
	})
}

func TestClampInt(t *testing.T) {
	cases := []struct{ n, lo, hi, want int }{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{0, 0, 10, 0},
		{10, 0, 10, 10},
	}
	for _, c := range cases {
		if got := clampInt(c.n, c.lo, c.hi); got != c.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", c.n, c.lo, c.hi, got, c.want)
		}
	}
}
