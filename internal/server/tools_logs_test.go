package server

import "testing"

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

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Errorf("firstNonEmpty returned %q, want x", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty() = %q, want empty", got)
	}
}
