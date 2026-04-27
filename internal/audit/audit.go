package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Per-value and total size caps on serialised args. Kept as unexported
// constants because SIEMs ingesting these records rely on a stable ceiling
// and env-tuning invites inconsistency across deployments.
const (
	maxArgStringBytes = 4 * 1024
	maxArgTotalBytes  = 16 * 1024
)

// Record captures one tool invocation. Populated by the Instrument
// middleware; produce-your-own is only useful from tests.
type Record struct {
	Timestamp   time.Time
	Caller      string         // OIDC subject or email; empty for unauthenticated paths
	TokenSource string         // "oauth" | "sso" | "" (how the caller authenticated)
	Tool        string         // tool name as registered with mcp-go
	Args        map[string]any // raw args as received from the client (see package doc on redaction + size cap)
	Outcome     string         // "ok" | "user_error" | "system_error" (see middleware.Classify)
	Duration    time.Duration
	Error       string // empty when Outcome=ok; handler error text or IsError result text
}

// Logger wraps an slog.Logger dedicated to the audit stream.
type Logger struct {
	slog *slog.Logger
}

// New builds a Logger backed by an slog.Handler. Production typically uses a
// JSON handler targeting stderr or a dedicated file; tests can pass a
// discard handler.
func New(h slog.Handler) *Logger {
	return &Logger{slog: slog.New(h)}
}

// NewJSON builds a Logger writing JSON records to w at info level. Convenience
// wrapper for the common "JSON to stderr" shape; use New for custom handlers.
func NewJSON(w io.Writer) *Logger {
	return New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Record emits the audit entry. Nil receiver is a deliberate no-op so
// callers don't need to nil-check every call site. trace_id and span_id
// are emitted as empty strings when no span is active on ctx.
func (l *Logger) Record(ctx context.Context, r Record) {
	if l == nil {
		return
	}
	args := capArgs(r.Args)
	traceID, spanID := traceIDs(ctx)
	l.slog.LogAttrs(ctx, slog.LevelInfo, "tool_call",
		slog.Time("timestamp", r.Timestamp),
		slog.String("caller", r.Caller),
		slog.String("caller_token_source", r.TokenSource),
		slog.String("tool", r.Tool),
		slog.Any("args", args),
		slog.String("outcome", r.Outcome),
		slog.Int64("duration_ms", r.Duration.Milliseconds()),
		slog.String("error", r.Error),
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	)
}

func traceIDs(ctx context.Context) (string, string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// capArgs enforces the per-value and total size caps on serialised args.
// Per-value cap recurses into nested map[string]any / []any. Total cap
// replaces the whole map with a "truncated" marker when the marshaled
// result still exceeds maxArgTotalBytes.
func capArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	capped, _ := capValue(args)
	out, _ := capped.(map[string]any)
	// Total-size cap — marshaled length is the source of truth (matches
	// what the JSON handler will serialise). Errors here can only happen
	// for unsupported types; treat them as "don't cap" and let slog surface
	// the real error downstream.
	b, err := json.Marshal(out)
	if err != nil || len(b) <= maxArgTotalBytes {
		return out
	}
	return map[string]any{"truncated": true, "bytes": len(b)}
}

// capValue copies-on-write: containers are cloned only when a nested
// truncation forces a change, so the caller's args map is never mutated.
// Returns (newValue, changed) — `changed` lets callers skip allocating a
// copy when nothing in this branch needed truncation. == on `any` panics
// for map/slice values, so we propagate `changed` explicitly instead.
func capValue(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		if len(x) <= maxArgStringBytes {
			return x, false
		}
		return fmt.Sprintf("%s…[truncated %d bytes]", x[:maxArgStringBytes], len(x)-maxArgStringBytes), true
	case map[string]any:
		var out map[string]any
		for k, val := range x {
			newVal, changed := capValue(val)
			if !changed {
				continue
			}
			if out == nil {
				out = make(map[string]any, len(x))
				maps.Copy(out, x)
			}
			out[k] = newVal
		}
		if out == nil {
			return x, false
		}
		return out, true
	case []any:
		var out []any
		for i, val := range x {
			newVal, changed := capValue(val)
			if !changed {
				continue
			}
			if out == nil {
				out = make([]any, len(x))
				copy(out, x)
			}
			out[i] = newVal
		}
		if out == nil {
			return x, false
		}
		return out, true
	default:
		return v, false
	}
}
