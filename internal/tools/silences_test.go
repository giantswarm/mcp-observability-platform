package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPaginateSilences_FilterActiveByDefault(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"s1","status":{"state":"active"}},
		{"id":"s2","status":{"state":"expired"}},
		{"id":"s3","status":{"state":"pending"}}
	]`)
	res, err := paginateSilences(raw, "", 0, 10)
	if err != nil {
		t.Fatalf("paginateSilences: %v", err)
	}
	b, _ := json.Marshal(res)
	body := string(b)
	if !strings.Contains(body, "s1") {
		t.Errorf("active silence s1 should be included: %s", body)
	}
	if strings.Contains(body, `"s2"`) || strings.Contains(body, `"s3"`) {
		t.Errorf("non-active silences should be filtered out by default: %s", body)
	}
}

func TestPaginateSilences_AllState(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"s1","status":{"state":"active"}},
		{"id":"s2","status":{"state":"expired"}}
	]`)
	res, err := paginateSilences(raw, "all", 0, 10)
	if err != nil {
		t.Fatalf("paginateSilences: %v", err)
	}
	b, _ := json.Marshal(res)
	body := string(b)
	if !strings.Contains(body, "s1") || !strings.Contains(body, "s2") {
		t.Errorf("state=all should include every silence: %s", body)
	}
}

func TestPaginateSilences_PageBoundaries(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"s1","status":{"state":"active"}},
		{"id":"s2","status":{"state":"active"}},
		{"id":"s3","status":{"state":"active"}},
		{"id":"s4","status":{"state":"active"}}
	]`)
	// pageSize=2, page=1 should give s3+s4
	res, err := paginateSilences(raw, "active", 1, 2)
	if err != nil {
		t.Fatalf("paginateSilences: %v", err)
	}
	b, _ := json.Marshal(res)
	body := string(b)
	if strings.Contains(body, `"s1"`) || strings.Contains(body, `"s2"`) {
		t.Errorf("page=1 size=2 should skip first page: %s", body)
	}
}
