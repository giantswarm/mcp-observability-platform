package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"time"
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
//
// Design rule: tools must not accept secret arguments. If a future tool
// needs a credential, look it up server-side from the caller identity
// rather than threading it through the tool params — that keeps the
// audit stream clean by construction and removes any need for
// per-argument redaction.
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

// Record emits the audit entry. Nil receiver is a deliberate no-op so that
// callers can stash a *Logger in a struct without nil-checking every call
// site; production code always passes a real logger.
func (l *Logger) Record(ctx context.Context, r Record) {
	if l == nil {
		return
	}
	args := capArgs(r.Args)
	l.slog.LogAttrs(ctx, slog.LevelInfo, "tool_call",
		slog.Time("timestamp", r.Timestamp),
		slog.String("caller", r.Caller),
		slog.String("caller_token_source", r.TokenSource),
		slog.String("tool", r.Tool),
		slog.Any("args", args),
		slog.String("outcome", r.Outcome),
		slog.Int64("duration_ms", r.Duration.Milliseconds()),
		slog.String("error", r.Error),
	)
}

// capArgs enforces the per-value and total size caps on serialised args.
// Per-value cap runs first (truncate strings >4 KiB in place on a lazy
// copy); total cap runs second (marshal + check — if the whole map still
// exceeds the total cap, replace with a short "truncated" marker).
//
// Returns the input unchanged when nothing exceeded the cap. Makes a copy
// only when a mutation is actually required, so the common small-args
// path stays allocation-free beyond what slog itself does.
func capArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	copied := false
	for k, v := range args {
		s, ok := v.(string)
		if !ok || len(s) <= maxArgStringBytes {
			continue
		}
		if !copied {
			cp := make(map[string]any, len(args))
			maps.Copy(cp, args)
			args = cp
			copied = true
		}
		args[k] = fmt.Sprintf("%s…[truncated %d bytes]", s[:maxArgStringBytes], len(s)-maxArgStringBytes)
	}
	// Total-size cap — marshaled length is the source of truth (matches
	// what the JSON handler will serialise). Errors here can only happen
	// for unsupported types; treat them as "don't cap" and let slog surface
	// the real error downstream.
	b, err := json.Marshal(args)
	if err != nil || len(b) <= maxArgTotalBytes {
		return args
	}
	return map[string]any{"truncated": true, "bytes": len(b)}
}
