// Package audit caps over-large tool-call argument values so they don't
// blow up downstream log pipelines. The Instrument middleware emits its
// own audit record via slog directly; this package is just the
// truncation helper.
//
// Per-string values > 4 KiB are replaced with a
// "<prefix>…[truncated N bytes]" marker (recursing into nested
// map[string]any / []any). If the marshaled total still exceeds 16 KiB
// the whole map is replaced with {"truncated": true, "bytes": N} —
// a SIEM seeing the marker can decide whether to fetch the original
// from a separate store, or accept the loss.
//
// Constants are unexported because SIEMs ingesting tool_call records
// rely on a stable ceiling; env-tuning invites inconsistency across
// deployments.
package audit

import (
	"encoding/json"
	"fmt"
	"maps"
)

const (
	maxArgStringBytes = 4 * 1024
	maxArgTotalBytes  = 16 * 1024
)

// TruncateArgs returns args with over-large values replaced by truncation
// markers. Copy-on-write: input is never mutated. nil in → nil out.
func TruncateArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	capped, _ := capValue(args)
	out, _ := capped.(map[string]any)
	// Total-size cap: marshaled length is the source of truth (matches
	// what the JSON handler will serialise). Errors here only fire on
	// unsupported types; treat as "don't cap" and let slog surface the
	// real error downstream.
	b, err := json.Marshal(out)
	if err != nil || len(b) <= maxArgTotalBytes {
		return out
	}
	return map[string]any{"truncated": true, "bytes": len(b)}
}

// capValue copies-on-write: containers are cloned only when a nested
// truncation forces a change, so caller-owned data is never mutated.
// `changed` lets callers skip the copy when nothing in this branch
// needed truncation. == on `any` panics for map/slice values, so we
// propagate `changed` explicitly instead of comparing.
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
