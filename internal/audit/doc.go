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
// args are emitted verbatim.
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
