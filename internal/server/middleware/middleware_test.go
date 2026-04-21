package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		res  *mcp.CallToolResult
		err  error
		want string
	}{
		{"no result, no error", nil, nil, OutcomeOK},
		{"success result", &mcp.CallToolResult{}, nil, OutcomeOK},
		{"Go error", nil, errors.New("boom"), OutcomeSystemError},
		{"Go error wins over IsError", &mcp.CallToolResult{IsError: true}, errors.New("x"), OutcomeSystemError},
		{"IsError result only", &mcp.CallToolResult{IsError: true}, nil, OutcomeUserError},
	}
	for _, c := range cases {
		if got := Classify(c.res, c.err); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTracing_PropagatesResultAndError(t *testing.T) {
	mw := Tracing()

	h := mw(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, nil
	})
	if _, err := h(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("ok path: %v", err)
	}

	h = mw(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("boom")
	})
	if _, err := h(context.Background(), mcp.CallToolRequest{}); err == nil {
		t.Fatalf("err path should propagate error")
	}
}

func TestMetrics_ExercisesAllThreeOutcomes(t *testing.T) {
	mw := Metrics()
	req := mcp.CallToolRequest{}
	req.Params.Name = "probe"

	handlers := []func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil },
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true}, nil
		},
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("boom")
		},
	}
	for _, handler := range handlers {
		// Exact counter / histogram values are asserted in observability's
		// /metrics scrape test; here we only ensure the middleware doesn't
		// panic on any outcome.
		_, _ = mw(handler)(context.Background(), req)
	}
}
