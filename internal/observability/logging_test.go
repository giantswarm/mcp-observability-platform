package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestInitLogging_NoEndpointReturnsNoopShutdownAndNilHandler(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")

	sh, h, err := InitLogging(context.Background(), "svc", "0.0.0")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h != nil {
		t.Errorf("handler should be nil when no endpoint is configured; got %T", h)
	}
	if sh == nil {
		t.Fatalf("shutdown should be a no-op, not nil")
	}
	if err := sh(context.Background()); err != nil {
		t.Errorf("noop shutdown returned err: %v", err)
	}
}

func TestInitLogging_UnknownProtocolReturnsError(t *testing.T) {
	// Endpoint present forces the code into the exporter-build path; the
	// invalid protocol then trips the explicit guard. Keeps operators out
	// of a "silent missing logs" failure mode.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "carrier-pigeon")

	_, _, err := InitLogging(context.Background(), "svc", "0.0.0")
	if err == nil {
		t.Fatalf("expected error for unknown protocol; got nil")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("error should quote the bad protocol value; got %v", err)
	}
}

func TestFanoutHandler_DispatchesToAllSinks(t *testing.T) {
	var a, b bytes.Buffer
	fan := FanoutHandler(
		slog.NewJSONHandler(&a, nil),
		slog.NewJSONHandler(&b, nil),
	)
	slog.New(fan).Info("hello", "k", "v")

	for _, buf := range []*bytes.Buffer{&a, &b} {
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("decode: %v (raw: %s)", err, buf.String())
		}
		if rec["msg"] != "hello" || rec["k"] != "v" {
			t.Errorf("sink saw %+v, want msg=hello k=v", rec)
		}
	}
}

func TestFanoutHandler_DropsNilSinks(t *testing.T) {
	// nil handlers should be filtered; a one-live-handler fanout should
	// return that handler directly to avoid wrapper overhead.
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	got := FanoutHandler(nil, base, nil)
	if got != base {
		t.Errorf("fanout of [nil, base, nil] should return base directly; got %T", got)
	}

	// Zero live handlers should return nil — callers branch on that.
	if FanoutHandler(nil, nil) != nil {
		t.Errorf("fanout of all-nil should be nil")
	}
}

func TestFanoutHandler_WithAttrsAndGroupPropagate(t *testing.T) {
	// WithAttrs / WithGroup must propagate to every child so per-logger
	// attribute chains don't drop on just one sink.
	var a, b bytes.Buffer
	fan := FanoutHandler(
		slog.NewJSONHandler(&a, nil),
		slog.NewJSONHandler(&b, nil),
	)
	logger := slog.New(fan).With("caller", "alice").WithGroup("req")
	logger.Info("hit", "path", "/mcp")

	for _, buf := range []*bytes.Buffer{&a, &b} {
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("decode: %v (raw: %s)", err, buf.String())
		}
		if rec["caller"] != "alice" {
			t.Errorf("WithAttrs dropped on sink: %+v", rec)
		}
		// WithGroup nests subsequent attrs under the group name.
		group, ok := rec["req"].(map[string]any)
		if !ok || group["path"] != "/mcp" {
			t.Errorf("WithGroup dropped on sink: %+v", rec)
		}
	}
}
