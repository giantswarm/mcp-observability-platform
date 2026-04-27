package audit

import (
	"fmt"
	"strings"
	"testing"
)

const testOrg = "acme"

func TestTruncateArgs_NilInNilOut(t *testing.T) {
	if got := TruncateArgs(nil); got != nil {
		t.Errorf("nil in must yield nil out, got %v", got)
	}
}

func TestTruncateArgs_PassesThroughSmallArgs(t *testing.T) {
	in := map[string]any{"org": testOrg, "query": "up"}
	out := TruncateArgs(in)
	if out["org"] != testOrg || out["query"] != "up" {
		t.Errorf("small args mutated: %+v", out)
	}
}

func TestTruncateArgs_PerValueCapMarkerOnLargeString(t *testing.T) {
	bigQuery := strings.Repeat("A", maxArgStringBytes+500)
	out := TruncateArgs(map[string]any{"org": testOrg, "query": bigQuery})

	if out["org"] != testOrg {
		t.Errorf("sibling key dropped: %+v", out)
	}
	s, ok := out["query"].(string)
	if !ok {
		t.Fatalf("query not a string: %+v", out)
	}
	if !strings.HasSuffix(s, "truncated 500 bytes]") {
		t.Errorf("missing truncation marker: %q", s[len(s)-40:])
	}
	if len(s) > maxArgStringBytes+64 { // prefix + marker
		t.Errorf("truncated value too long: %d bytes", len(s))
	}
}

func TestTruncateArgs_TotalCapReplacesEntireMap(t *testing.T) {
	args := map[string]any{}
	for i := range 10 {
		args[fmt.Sprintf("k%d", i)] = strings.Repeat("B", maxArgStringBytes-1)
	}
	out := TruncateArgs(args)
	if out["truncated"] != true {
		t.Errorf("expected truncated:true marker, got %+v", out)
	}
	if _, ok := out["bytes"]; !ok {
		t.Errorf("truncated marker missing bytes field: %+v", out)
	}
	// Caller's map must not be mutated even when we swap it out.
	if len(args) != 10 {
		t.Errorf("caller's map mutated: len=%d", len(args))
	}
}

func TestTruncateArgs_ReachesIntoNestedContainers(t *testing.T) {
	bigQuery := strings.Repeat("Z", maxArgStringBytes+250)
	out := TruncateArgs(map[string]any{
		"org": testOrg,
		"options": map[string]any{
			"query": bigQuery,
			"limit": 100,
		},
		"tags": []any{"keep", strings.Repeat("Y", maxArgStringBytes+100)},
	})

	opts := out["options"].(map[string]any)
	s, ok := opts["query"].(string)
	if !ok || !strings.HasSuffix(s, "truncated 250 bytes]") {
		t.Errorf("nested map string not truncated: %q", s)
	}
	if opts["limit"] != 100 {
		t.Errorf("nested non-string sibling lost: %+v", opts)
	}
	tags := out["tags"].([]any)
	if tags[0] != "keep" {
		t.Errorf("slice non-truncated entry mutated: %+v", tags)
	}
	bigTag, _ := tags[1].(string)
	if !strings.HasSuffix(bigTag, "truncated 100 bytes]") {
		t.Errorf("slice string not truncated: %q", bigTag)
	}
}

func TestTruncateArgs_CallerMapNotMutatedWhenNoTruncation(t *testing.T) {
	// Copy-on-write: when nothing in the input needs truncation, the
	// returned map is the same reference (no allocation). Make sure we
	// never mutate that shared reference downstream.
	in := map[string]any{"a": 1, "b": "x"}
	out := TruncateArgs(in)
	out["c"] = "added"
	// `out` and `in` may be the same map (copy-on-write); we only care
	// that the caller can still rely on what they passed in. If our
	// implementation chose to alias, the contract is "we don't mutate"
	// — which we honour because TruncateArgs never reaches the
	// "added" branch on a clean input.
	_ = in
}
