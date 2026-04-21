package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestChain_OrderIsOuterToInner(t *testing.T) {
	var trace []string
	mw := func(tag string) Middleware {
		return func(h Handler) Handler {
			return func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				trace = append(trace, "pre:"+tag)
				res, err := h(ctx, r)
				trace = append(trace, "post:"+tag)
				return res, err
			}
		}
	}

	h := Chain(mw("A"), mw("B"), mw("C"))(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		trace = append(trace, "handler")
		return nil, nil
	})
	_, _ = h(context.Background(), mcp.CallToolRequest{})

	want := []string{"pre:A", "pre:B", "pre:C", "handler", "post:C", "post:B", "post:A"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Errorf("trace[%d] = %q, want %q", i, trace[i], want[i])
		}
	}
}

func TestDefault_WrapsMetricsAndTracing_NoPanic(t *testing.T) {
	// Smoke test: Default(name, nil) returns a Middleware that doesn't panic
	// when applied to a trivial handler. Exercises metric+span code paths
	// on both success and error outcomes.
	h := Default("test", nil)(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, nil
	})
	if _, err := h(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("default wrap ok path: %v", err)
	}
	h = Default("test", nil)(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("boom")
	})
	if _, err := h(context.Background(), mcp.CallToolRequest{}); err == nil {
		t.Fatalf("default wrap err path should propagate error")
	}
}

func TestPaginateStrings(t *testing.T) {
	all := []string{"zeta", "alpha", "beta", "gamma", "delta"}
	got := PaginateStrings(all, "", 0, 2)
	if got.Total != 5 || got.Items[0] != "alpha" || got.Items[1] != "beta" {
		t.Errorf("page 0 = %+v", got)
	}
	got = PaginateStrings(all, "ET", 0, 10)
	if got.Total != 2 {
		t.Errorf("prefix match got %d, want 2", got.Total)
	}
}

func TestClampInt(t *testing.T) {
	if ClampInt(11, 0, 10) != 10 || ClampInt(-1, 0, 10) != 0 || ClampInt(5, 0, 10) != 5 {
		t.Errorf("ClampInt broken")
	}
}

func TestIntArg_MultipleFormats(t *testing.T) {
	for _, m := range []map[string]any{
		{"n": float64(42)}, {"n": 42}, {"n": int64(42)}, {"n": "42"},
	} {
		if IntArg(m, "n") != 42 {
			t.Errorf("IntArg(%v) != 42", m)
		}
	}
}

func TestEnforceResponseCap(t *testing.T) {
	t.Setenv("TOOL_MAX_RESPONSE_BYTES", "100")
	if EnforceResponseCap([]byte("small")) != nil {
		t.Errorf("under-limit should be nil")
	}
	e := EnforceResponseCap(make([]byte, 101))
	if e == nil || e.Error != "response_too_large" {
		t.Errorf("over-limit = %+v", e)
	}
	if _, err := json.Marshal(e); err != nil {
		t.Errorf("json.Marshal: %v", err)
	}
}
