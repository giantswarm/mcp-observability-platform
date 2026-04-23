package middleware

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func callTimeout(t *testing.T, parent context.Context, timeout time.Duration, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	t.Helper()
	wrapped := ToolTimeout(timeout)(handler)
	req := mcp.CallToolRequest{}
	req.Params.Name = "test_tool"
	return wrapped(parent, req)
}

func TestToolTimeout_ReturnsIsErrorOnDeadline(t *testing.T) {
	var finished bool
	res, err := callTimeout(t, context.Background(), 50*time.Millisecond, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			finished = true
			return mcp.NewToolResultText("done"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("middleware must convert timeout to IsError result, not propagate err; got %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError=true result; got %+v", res)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "exceeded timeout") || !strings.Contains(text, "50ms") {
		t.Errorf("timeout text missing expected phrases: %q", text)
	}
	if finished {
		t.Errorf("handler should have been cut short by the timeout")
	}
}

func TestToolTimeout_PassesThroughFastHandler(t *testing.T) {
	res, err := callTimeout(t, context.Background(), time.Second, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("fast"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Errorf("fast handler marked IsError")
	}
	if res.Content[0].(mcp.TextContent).Text != "fast" {
		t.Errorf("content mutated")
	}
}

func TestToolTimeout_ZeroDurationDisables(t *testing.T) {
	var sawDeadline bool
	_, err := callTimeout(t, context.Background(), 0, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline = true
		}
		return mcp.NewToolResultText("ok"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sawDeadline {
		t.Errorf("ctx should have no deadline when timeout=0")
	}
}

func TestToolTimeout_PreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling the middleware

	res, err := callTimeout(t, parent, 5*time.Second, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil {
		t.Fatalf("expected parent cancellation to propagate, got nil err")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result on parent-cancel path, got %+v", res)
	}
}

func TestToolTimeout_HandlerErrorNotMaskedAsTimeout(t *testing.T) {
	wantErr := errors.New("grafana: upstream 502")
	res, err := callTimeout(t, context.Background(), time.Second, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("handler error should pass through unchanged; got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result when handler errors, got %+v", res)
	}
}
