package middleware

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func callTimeout(t *testing.T, parent context.Context, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	t.Helper()
	wrapped := ToolTimeout()(handler)
	req := mcp.CallToolRequest{}
	req.Params.Name = "test_tool"
	return wrapped(parent, req)
}

func TestToolTimeout_ReturnsIsErrorOnDeadline(t *testing.T) {
	t.Setenv("TOOL_TIMEOUT", "50ms")
	var finished bool
	res, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	t.Setenv("TOOL_TIMEOUT", "1s")
	res, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	t.Setenv("TOOL_TIMEOUT", "0")
	var sawDeadline bool
	_, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline = true
		}
		return mcp.NewToolResultText("ok"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sawDeadline {
		t.Errorf("ctx should have no deadline when TOOL_TIMEOUT=0")
	}
}

func TestToolTimeout_UsesDefaultWhenUnset(t *testing.T) {
	t.Setenv("TOOL_TIMEOUT", "")
	var gotDeadline time.Duration
	_, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if d, ok := ctx.Deadline(); ok {
			gotDeadline = time.Until(d)
		}
		return mcp.NewToolResultText("ok"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Deadline should be close to DefaultToolTimeout (30s) — allow generous slack.
	if gotDeadline < DefaultToolTimeout-5*time.Second || gotDeadline > DefaultToolTimeout {
		t.Errorf("deadline = %s, want close to %s", gotDeadline, DefaultToolTimeout)
	}
}

func TestToolTimeout_FallsBackOnMalformedValue(t *testing.T) {
	t.Setenv("TOOL_TIMEOUT", "not-a-duration")
	var gotDeadline time.Duration
	_, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if d, ok := ctx.Deadline(); ok {
			gotDeadline = time.Until(d)
		}
		return mcp.NewToolResultText("ok"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotDeadline < DefaultToolTimeout-5*time.Second || gotDeadline > DefaultToolTimeout {
		t.Errorf("malformed TOOL_TIMEOUT should fall back to default; got deadline %s", gotDeadline)
	}
}

func TestToolTimeout_PreservesParentCancellation(t *testing.T) {
	t.Setenv("TOOL_TIMEOUT", "5s")
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling the middleware

	res, err := callTimeout(t, parent, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	t.Setenv("TOOL_TIMEOUT", "1s")
	wantErr := errors.New("grafana: upstream 502")
	res, err := callTimeout(t, context.Background(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("handler error should pass through unchanged; got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result when handler errors, got %+v", res)
	}
}
