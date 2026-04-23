package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLokiPageCursor_LimitNotHit(t *testing.T) {
	body := []byte(`{"data":{"result":[
		{"values":[["1700000001","a"],["1700000000","b"]]}
	]}}`)
	cursor, hit := lokiPageCursor(body, 100)
	if cursor != "" || hit {
		t.Errorf("under-limit: got cursor=%q hit=%v, want empty/false", cursor, hit)
	}
}

func TestLokiPageCursor_LimitHit_ReturnsOldest(t *testing.T) {
	body := []byte(`{"data":{"result":[
		{"values":[["1700000005","a"],["1700000003","b"]]},
		{"values":[["1700000002","c"]]}
	]}}`)
	cursor, hit := lokiPageCursor(body, 3)
	if !hit {
		t.Errorf("expected limit-hit = true")
	}
	if cursor != "1700000002" {
		t.Errorf("cursor = %q, want oldest 1700000002", cursor)
	}
}

func TestLokiPageCursor_MalformedInput(t *testing.T) {
	cursor, hit := lokiPageCursor([]byte("not json"), 100)
	if cursor != "" || hit {
		t.Errorf("malformed input should yield empty cursor, got %q %v", cursor, hit)
	}
}

func TestProjectLokiPatterns(t *testing.T) {
	raw := json.RawMessage(`{
		"status":"success",
		"data":[
			{"pattern":"error X","samples":[[1700000000,5],[1700000001,3]]},
			{"pattern":"warn Y","samples":[[1700000000,10]]},
			{"pattern":"low Z","samples":[[1700000000,1]]}
		]
	}`)
	// limit=2 should slice top-N
	got, err := projectLokiPatterns(raw, 2)
	if err != nil {
		t.Fatalf("projectLokiPatterns: %v", err)
	}
	b, _ := json.Marshal(got)
	body := string(b)
	if !strings.Contains(body, "warn Y") {
		t.Errorf("expected highest-count pattern 'warn Y' in top-2: %s", body)
	}
	if strings.Contains(body, "low Z") {
		t.Errorf("limit=2 should exclude lowest-count 'low Z': %s", body)
	}
}

func TestProjectLokiPatterns_MalformedInput(t *testing.T) {
	_, err := projectLokiPatterns(json.RawMessage(`{broken`), 10)
	if err == nil {
		t.Fatalf("expected unmarshal error, got nil")
	}
}
