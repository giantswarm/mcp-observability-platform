package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestResponseCap_PassesThroughSmallResults(t *testing.T) {
	h := ResponseCap(100)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"ok":true}`}}}, nil
	})
	res, err := h(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError = true; under-limit response should not be marked as error")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if text != `{"ok":true}` {
		t.Errorf("content mutated; got %q", text)
	}
}

func TestResponseCap_ReplacesOversizedWithStructuredError(t *testing.T) {
	oversized := strings.Repeat("A", 150)
	h := ResponseCap(100)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: oversized}}}, nil
	})
	res, err := h(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false; over-limit response should be marked as error so the LLM sees an actionable failure")
	}
	// Body is the structured cap payload.
	var got responseCapError
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &got); err != nil {
		t.Fatalf("unmarshal cap payload: %v", err)
	}
	if got.Error != "response_too_large" || got.Bytes != 150 || got.Limit != 100 {
		t.Errorf("cap payload = %+v", got)
	}
	if got.Hint == "" {
		t.Errorf("cap payload missing hint; LLM clients rely on this to narrow the query")
	}
}

func TestResponseCap_DisabledWhenLimitZero(t *testing.T) {
	huge := strings.Repeat("A", 1_000_000)
	h := ResponseCap(0)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: huge}}}, nil
	})
	res, _ := h(context.Background(), mcp.CallToolRequest{})
	if res.IsError {
		t.Errorf("cap=0 should disable capping; huge response passed through")
	}
	if res.Content[0].(mcp.TextContent).Text != huge {
		t.Errorf("content mutated despite cap=0")
	}
}

func TestResponseCap_PassesThroughHandlerError(t *testing.T) {
	h := ResponseCap(100)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, context.Canceled
	})
	res, err := h(context.Background(), mcp.CallToolRequest{})
	if err == nil {
		t.Errorf("handler error swallowed")
	}
	if res != nil {
		t.Errorf("result returned despite handler error: %+v", res)
	}
}
