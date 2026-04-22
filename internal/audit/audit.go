// Package audit emits a structured record per tool call for compliance + post-
// hoc investigation. Records carry caller identity, tool name, input args,
// outcome, and duration in JSON at info level so security-side tooling can
// ingest them without guessing schema.
//
// Audit is separate from the debug "tool call" slog line on purpose:
//   - always on (not gated by --debug),
//   - stable schema (fields never change shape silently),
//   - routable to its own sink (stderr today, a dedicated file or SIEM tomorrow).
//
// # Privacy
//
// Caller is the OIDC subject or email. Treat the audit stream as containing
// PII and route it to a store with appropriate retention controls.
//
// Args is the raw map the client sent. Today's read-only tool surface does
// not take secrets (orgs, dashboard UIDs, PromQL/LogQL, time ranges), so
// args are emitted verbatim. When a future tool carries sensitive input,
// install a Redactor on the Logger and drop/mask the relevant keys before
// they reach the sink.
//
// # Size cap
//
// Large tool arguments (a pasted 1 MB LogQL query, a multi-MB JSON payload)
// would otherwise emit audit lines far above Loki's 256 KiB default ingest
// limit and blow up the audit stream. Every string value >4 KiB is
// replaced with "<prefix>…[truncated N bytes]"; if the serialised args
// still exceed 16 KiB total, the whole map is replaced with
// {"truncated": true, "bytes": N}. Thresholds are constants, not
// env-tunable — stable SIEM ingest beats per-deployment knobs.
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

// Redactor optionally mutates args before they are emitted. Return a new map
// or the same map mutated in place; the Logger does not share the map with
// the caller after Record returns.
type Redactor func(args map[string]any) map[string]any

// Logger wraps an slog.Logger dedicated to the audit stream.
type Logger struct {
	slog   *slog.Logger
	redact Redactor
}

// Option configures a Logger.
type Option func(*Logger)

// WithRedactor installs a Redactor applied to Record.Args before each emit.
// Use this when a tool accepts a sensitive argument (a bearer token, an
// API key, a password) that should never appear in the audit stream.
func WithRedactor(r Redactor) Option {
	return func(l *Logger) { l.redact = r }
}

// New builds a Logger backed by an slog.Handler. Production typically uses a
// JSON handler targeting stderr or a dedicated file; tests can pass a
// discard handler.
func New(h slog.Handler, opts ...Option) *Logger {
	l := &Logger{slog: slog.New(h)}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// NewJSON builds a Logger writing JSON records to w at info level. Convenience
// wrapper for the common "JSON to stderr" shape; use New for custom handlers.
func NewJSON(w io.Writer, opts ...Option) *Logger {
	return New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}), opts...)
}

// Record emits the audit entry. Nil receiver is a deliberate no-op so that
// callers can stash a *Logger in a struct without nil-checking every call
// site; production code always passes a real logger.
func (l *Logger) Record(ctx context.Context, r Record) {
	if l == nil {
		return
	}
	args := r.Args
	if l.redact != nil && args != nil {
		// Pass a defensive copy to the redactor so handler-side maps aren't
		// mutated by audit-side logic. Cheaper than cloning on every call-
		// site and keeps the contract simple.
		cp := make(map[string]any, len(args))
		maps.Copy(cp, args)
		args = l.redact(cp)
	}
	args = capArgs(args)
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
