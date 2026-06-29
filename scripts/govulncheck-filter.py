#!/usr/bin/env python3
"""Filter govulncheck JSON output against an allowlist of accepted vuln IDs.

Reads govulncheck's NDJSON stream on stdin (produced by `govulncheck -format json`,
which always exits 0). Considers only vulnerabilities with a reachable, function-level
call trace (matching govulncheck's "Symbol Results"). IDs listed in the GOVULN_IGNORE
env var (space- or comma-separated) are reported as accepted exceptions; any remaining
vulnerability causes a non-zero exit so CI still fails on new or other findings.
"""
import json
import os
import sys

def iter_objects(text):
    """Yield each JSON object from govulncheck's stream of concatenated,
    pretty-printed (multi-line) objects — it is not newline-delimited JSON."""
    decoder = json.JSONDecoder()
    idx = 0
    length = len(text)
    while idx < length:
        while idx < length and text[idx].isspace():
            idx += 1
        if idx >= length:
            break
        obj, idx = decoder.raw_decode(text, idx)
        yield obj


ignore = set(os.environ.get("GOVULN_IGNORE", "").replace(",", " ").split())
summaries = {}  # osv id -> summary
reachable = {}  # osv id -> True if any finding has a function-level trace

for msg in iter_objects(sys.stdin.read()):
    if "osv" in msg:
        osv = msg["osv"]
        summaries[osv["id"]] = osv.get("summary", "")
    elif "finding" in msg:
        finding = msg["finding"]
        oid = finding["osv"]
        trace = finding.get("trace", [])
        is_reachable = bool(trace) and bool(trace[0].get("function"))
        reachable[oid] = reachable.get(oid, False) or is_reachable

found = {oid for oid, reach in reachable.items() if reach}

for oid in sorted(found & ignore):
    print(f"IGNORED (allowlisted): {oid} {summaries.get(oid, '')}".rstrip())

unexpected = sorted(found - ignore)
if unexpected:
    print("\nVulnerabilities found:")
    for oid in unexpected:
        print(f"  {oid}: {summaries.get(oid, '')}  https://pkg.go.dev/vuln/{oid}")
    sys.exit(1)

print("No actionable vulnerabilities (allowlist applied).")
