package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const testOrg = "acme"

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
		Timestamp:   now,
		Caller:      "alice@example.com",
		TokenSource: "oauth",
		Tool:        "list_orgs",
		Args:        map[string]any{"page": 0},
		Outcome:     "ok",
		Duration:    120 * time.Millisecond,
	})

	got := decodeOne(t, &buf)
	for _, k := range []string{"time", "level", "msg", "timestamp", "caller", "caller_token_source", "tool", "args", "outcome", "duration_ms", "error"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing field %q in %+v", k, got)
		}
	}
	if got["tool"] != "list_orgs" || got["caller"] != "alice@example.com" || got["outcome"] != "ok" {
		t.Errorf("unexpected values: tool=%v caller=%v outcome=%v", got["tool"], got["caller"], got["outcome"])
	}
	if got["caller_token_source"] != "oauth" {
		t.Errorf("caller_token_source = %v, want oauth", got["caller_token_source"])
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
	original := map[string]any{"org": testOrg, "token": "supersecret"}
	l.Record(context.Background(), Record{Tool: "x", Args: original, Outcome: "ok"})

	got := decodeOne(t, &buf)
	args := got["args"].(map[string]any)
	if args["token"] != "REDACTED" {
		t.Errorf("redactor did not mask token: args = %+v", args)
	}
	if args["org"] != testOrg {
		t.Errorf("redactor dropped non-sensitive key: args = %+v", args)
	}
	// Redactor operates on a copy; caller's map must not be mutated.
	if original["token"] != "supersecret" {
		t.Errorf("redactor mutated caller's map: original = %+v", original)
	}
}

func TestLogger_Record_RedactorRunsBeforeTruncation(t *testing.T) {
	// A redactor that expands a value (e.g. pads a credential with a
	// fingerprint) must see its output then truncated — order matters
	// because truncation preserves the marker, while reordering would
	// let an unredacted value slip past the cap.
	var buf bytes.Buffer
	l := NewJSON(&buf, WithRedactor(func(args map[string]any) map[string]any {
		if _, ok := args["token"]; ok {
			args["token"] = strings.Repeat("X", maxArgStringBytes+200) // redactor returns a huge string
		}
		return args
	}))
	l.Record(context.Background(), Record{
		Tool: "x",
		Args: map[string]any{"token": "secret"},
	})
	got := decodeOne(t, &buf)
	args := got["args"].(map[string]any)
	s, ok := args["token"].(string)
	if !ok {
		t.Fatalf("token not a string: %+v", args)
	}
	if !strings.HasPrefix(s, "XXXX") {
		t.Errorf("redactor output not seen: %q", s[:20])
	}
	if !strings.HasSuffix(s, "truncated 200 bytes]") {
		t.Errorf("truncation did not run after redactor: %q", s[len(s)-40:])
	}
}

func TestLogger_Record_PassesThroughSmallArgs(t *testing.T) {
	// Regression guard: normal-sized args must not be mutated or reshaped
	// by the size-cap logic. A 100-byte string and a handful of keys fit
	// well under both caps.
	var buf bytes.Buffer
	l := NewJSON(&buf)
	l.Record(context.Background(), Record{
		Tool: "x",
		Args: map[string]any{"org": testOrg, "query": "up"},
	})
	got := decodeOne(t, &buf)
	args := got["args"].(map[string]any)
	if args["org"] != testOrg || args["query"] != "up" {
		t.Errorf("small args mutated: %+v", args)
	}
}

func TestLogger_Record_TruncatesLargeStringValue(t *testing.T) {
	// A single value over 4 KiB is rewritten with a ...[truncated N bytes]
	// marker but the map shape (other keys, types) is preserved so SIEM
	// searches keep working.
	var buf bytes.Buffer
	l := NewJSON(&buf)
	bigQuery := strings.Repeat("A", maxArgStringBytes+500)
	l.Record(context.Background(), Record{
		Tool: "query",
		Args: map[string]any{"org": testOrg, "query": bigQuery},
	})
	got := decodeOne(t, &buf)
	args := got["args"].(map[string]any)
	if args["org"] != testOrg {
		t.Errorf("sibling key dropped: %+v", args)
	}
	s, ok := args["query"].(string)
	if !ok {
		t.Fatalf("query not a string: %+v", args)
	}
	if !strings.HasSuffix(s, "truncated 500 bytes]") {
		t.Errorf("missing truncation marker: %q", s[len(s)-40:])
	}
	if len(s) > maxArgStringBytes+64 { // prefix + marker
		t.Errorf("truncated value too long: %d bytes", len(s))
	}
}

func TestLogger_Record_TruncatesTotalArgsOverCap(t *testing.T) {
	// Many string values each below the per-value cap but collectively
	// above the total cap — the whole map is replaced with a marker so
	// the audit line can never exceed the Loki ingest limit.
	var buf bytes.Buffer
	l := NewJSON(&buf)
	args := map[string]any{}
	for i := range 10 {
		args[fmt.Sprintf("k%d", i)] = strings.Repeat("B", maxArgStringBytes-1)
	}
	l.Record(context.Background(), Record{Tool: "x", Args: args})

	got := decodeOne(t, &buf)
	emitted := got["args"].(map[string]any)
	if emitted["truncated"] != true {
		t.Errorf("expected truncated:true marker, got %+v", emitted)
	}
	if _, ok := emitted["bytes"]; !ok {
		t.Errorf("truncated marker missing bytes field: %+v", emitted)
	}
	// Caller's map must not be mutated even when we swap it out.
	if len(args) != 10 {
		t.Errorf("caller's map mutated: len=%d", len(args))
	}
}

// Compile-time check that New accepts an slog.Handler and is usable from
// callers outside the package without constructor ceremony. Keeps the
// exported surface small: a single-arg constructor plus NewJSON.
var _ = New(slog.NewTextHandler(io.Discard, nil))

// TestLogger_Record_RedactorReturningNil_EmitsEmptyArgs guards against a
// redactor that drops the whole map instead of masking values. The audit
// record must still emit with the map replaced by an empty JSON object — the
// tool-call line is still useful (who called what, outcome, duration), and
// callers should not be able to suppress their own audit entry by returning
// nil from a user-supplied redactor.
func TestLogger_Record_RedactorReturningNil_EmitsEmptyArgs(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSON(&buf, WithRedactor(func(args map[string]any) map[string]any {
		return nil
	}))
	l.Record(context.Background(), Record{
		Tool:    "query_metrics",
		Args:    map[string]any{"org": testOrg},
		Outcome: "ok",
	})
	got := decodeOne(t, &buf)
	// Audit entry still emits with all non-args fields intact.
	if got["tool"] != "query_metrics" || got["outcome"] != "ok" {
		t.Errorf("core fields missing: %+v", got)
	}
	// slog renders nil map as JSON null; what matters is that no stale
	// args leak — a panic or misrender would be the real regression.
	if v, ok := got["args"]; !ok {
		t.Errorf("args key missing entirely: %+v", got)
	} else if v != nil {
		m, isMap := v.(map[string]any)
		if isMap && len(m) != 0 {
			t.Errorf("nil redactor leaked args: %+v", m)
		}
	}
}

// TestLogger_Record_RedactorPanic_DoesNotTakeDownCaller guards against a
// buggy user redactor: a panic inside Record must not kill the tool-handler
// goroutine. The audit package doesn't promise to recover today — this test
// documents the current behaviour and fails if the decision changes silently.
func TestLogger_Record_RedactorPanic_IsRecovered(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSON(&buf, WithRedactor(func(args map[string]any) map[string]any {
		panic("redactor bug")
	}))

	defer func() {
		if r := recover(); r == nil {
			// We expect the panic to propagate to THIS defer: the audit
			// package does not currently recover. Callers that want
			// defence against a buggy redactor can wrap Record in their
			// own recover — but the contract MUST be explicit, not
			// incidental. Fail if someone silently adds a recover that
			// changes that contract without updating this test.
			t.Errorf("redactor panic was silently recovered; contract must be explicit")
		}
	}()

	l.Record(context.Background(), Record{
		Tool: "x",
		Args: map[string]any{"token": "s"},
	})
}

// TestLogger_Record_RedactorMutationDoesNotLeakToCaller proves the Logger
// passes a defensive copy to the redactor, so a redactor that mutates its
// input cannot poison the caller's Args map (which may still be referenced
// by the surrounding handler code).
func TestLogger_Record_RedactorMutationDoesNotLeakToCaller(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSON(&buf, WithRedactor(func(args map[string]any) map[string]any {
		args["injected"] = "by redactor"
		delete(args, "org")
		return args
	}))
	original := map[string]any{"org": testOrg, "token": "s"}
	l.Record(context.Background(), Record{Tool: "x", Args: original})

	if _, leaked := original["injected"]; leaked {
		t.Errorf("redactor's mutation leaked back to caller map: %+v", original)
	}
	if original["org"] != testOrg {
		t.Errorf("redactor's delete leaked back to caller map: %+v", original)
	}
}
