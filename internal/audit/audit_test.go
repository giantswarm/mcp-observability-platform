package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v (raw: %s)", err, buf.String())
	}
	return got
}

func TestLogger_Record_EmitsStableSchema(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSON(&buf)

	now := time.Unix(1700000000, 0).UTC()
	l.Record(context.Background(), Record{
		Timestamp: now,
		Caller:    "alice@example.com",
		Tool:      "list_orgs",
		Args:      map[string]any{"page": 0},
		Outcome:   "ok",
		Duration:  120 * time.Millisecond,
	})

	got := decodeOne(t, &buf)
	for _, k := range []string{"time", "level", "msg", "timestamp", "caller", "tool", "args", "outcome", "duration_ms", "error"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing field %q in %+v", k, got)
		}
	}
	if got["tool"] != "list_orgs" || got["caller"] != "alice@example.com" || got["outcome"] != "ok" {
		t.Errorf("unexpected values: tool=%v caller=%v outcome=%v", got["tool"], got["caller"], got["outcome"])
	}
	if got["duration_ms"].(float64) != 120 {
		t.Errorf("duration_ms = %v, want 120", got["duration_ms"])
	}
	if got["msg"] != "tool_call" {
		t.Errorf("msg = %v, want tool_call", got["msg"])
	}
}

func TestLogger_Record_CarriesErrorAcrossOutcomes(t *testing.T) {
	cases := []struct {
		name, outcome, errText string
	}{
		{"user_error", "user_error", "missing required argument 'org'"},
		{"system_error", "system_error", "grafana datasource proxy failed: 502"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := NewJSON(&buf)
			l.Record(context.Background(), Record{
				Timestamp: time.Now(),
				Tool:      "query_metrics",
				Outcome:   c.outcome,
				Error:     c.errText,
			})
			got := decodeOne(t, &buf)
			if got["outcome"] != c.outcome || got["error"] != c.errText {
				t.Errorf("%s path = %+v", c.name, got)
			}
		})
	}
}

func TestLogger_Record_NilReceiverIsNoop(t *testing.T) {
	var l *Logger
	// Must not panic; if it did, the test process would die rather than fail.
	l.Record(context.Background(), Record{Tool: "noop"})
}

func TestWithRedactor_MasksSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSON(&buf, WithRedactor(func(args map[string]any) map[string]any {
		if _, ok := args["token"]; ok {
			args["token"] = "REDACTED"
		}
		return args
	}))
	original := map[string]any{"org": "acme", "token": "supersecret"}
	l.Record(context.Background(), Record{Tool: "x", Args: original, Outcome: "ok"})

	got := decodeOne(t, &buf)
	args := got["args"].(map[string]any)
	if args["token"] != "REDACTED" {
		t.Errorf("redactor did not mask token: args = %+v", args)
	}
	if args["org"] != "acme" {
		t.Errorf("redactor dropped non-sensitive key: args = %+v", args)
	}
	// Redactor operates on a copy; caller's map must not be mutated.
	if original["token"] != "supersecret" {
		t.Errorf("redactor mutated caller's map: original = %+v", original)
	}
}

// Compile-time check that New accepts an slog.Handler and is usable from
// callers outside the package without constructor ceremony. Keeps the
// exported surface small: a single-arg constructor plus NewJSON.
var _ = New(slog.NewTextHandler(io.Discard, nil))
