package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
)

// stubRequest builds a CallToolRequest with the given tool name and args.
// Mirrors how mcp-go populates Params at dispatch time; without this the
// middleware reads an empty tool name and the test isn't verifying the
// real path.
func stubRequest(name string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

func decodeAuditLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("expected one JSON record, got %q: %v", buf.String(), err)
	}
	return rec
}

// applyHandler runs Instrument around a fake handler. Returns the decoded
// audit line emitted by the composite middleware.
func applyHandler(t *testing.T, h server.ToolHandlerFunc, name string, args map[string]any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	log := audit.NewJSON(&buf)
	wrapped := Instrument(log)(h)
	_, _ = wrapped(context.Background(), stubRequest(name, args))
	return decodeAuditLine(t, &buf)
}

func TestInstrument_SuccessRecordsOK(t *testing.T) {
	rec := applyHandler(t,
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "ok"}},
			}, nil
		},
		"list_orgs", map[string]any{"org": "acme"})

	if rec["tool"] != "list_orgs" {
		t.Errorf("tool = %v, want list_orgs (reads req.Params.Name)", rec["tool"])
	}
	if rec["outcome"] != OutcomeOK {
		t.Errorf("outcome = %v, want %s", rec["outcome"], OutcomeOK)
	}
	if rec["error"] != "" {
		t.Errorf("error = %v, want empty on success", rec["error"])
	}
	args := rec["args"].(map[string]any)
	if args["org"] != "acme" {
		t.Errorf("args not captured: %+v", args)
	}
}

func TestInstrument_HandlerErrorRecordsSystemError(t *testing.T) {
	rec := applyHandler(t,
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("downstream 502")
		},
		"query_metrics", nil)

	if rec["outcome"] != OutcomeSystemError {
		t.Errorf("outcome = %v, want %s (Go error → system)", rec["outcome"], OutcomeSystemError)
	}
	if rec["error"] != "downstream 502" {
		t.Errorf("error = %v, want 'downstream 502'", rec["error"])
	}
}

func TestInstrument_IsErrorResultRecordsUserError(t *testing.T) {
	rec := applyHandler(t,
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "missing required argument 'org'"}},
			}, nil
		},
		"list_datasources", nil)

	if rec["outcome"] != OutcomeUserError {
		t.Errorf("outcome = %v, want %s (IsError → user)", rec["outcome"], OutcomeUserError)
	}
	if rec["error"] != "missing required argument 'org'" {
		t.Errorf("error = %v, want the IsError text", rec["error"])
	}
}

func TestInstrument_IsErrorWithNoContentRecordsPlaceholder(t *testing.T) {
	rec := applyHandler(t,
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true}, nil
		},
		"x", nil)
	if rec["error"] != "tool returned isError with no text content" {
		t.Errorf("error = %v", rec["error"])
	}
}

func TestInstrument_NilLoggerIsPassthrough(t *testing.T) {
	// audit.Logger.Record is nil-safe, so Instrument(nil) must still drive
	// the handler and emit span + metric side-effects without panicking.
	called := false
	h := Instrument(nil)(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return nil, nil
	})
	if _, err := h(context.Background(), stubRequest("x", nil)); err != nil {
		t.Fatalf("passthrough returned err: %v", err)
	}
	if !called {
		t.Fatal("nil-logger middleware did not call the handler")
	}
}

// Ensure Instrument doesn't panic on any of the three outcomes — exact
// counter / histogram values are asserted in the observability /metrics
// scrape test; here we only guard the composite's survival.
func TestInstrument_ExercisesAllThreeOutcomes(t *testing.T) {
	mw := Instrument(nil)
	req := stubRequest("probe", nil)

	handlers := []server.ToolHandlerFunc{
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil },
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true}, nil
		},
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("boom")
		},
	}
	for _, handler := range handlers {
		_, _ = mw(handler)(context.Background(), req)
	}
}
