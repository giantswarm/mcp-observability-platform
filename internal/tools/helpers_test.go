package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsValidPromIdent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"http_requests_total", true},
		{"foo:sum_rate", true},
		{"_underscore_prefix", true},
		{"Capital", true},
		{"123numeric_start", false},
		{"has-dash", false},
		{"has space", false},
		{"has{brace", false},
		{"with.dot", false},
		{"x", true},
	}
	for _, c := range cases {
		if got := isValidPromIdent(c.in); got != c.want {
			t.Errorf("isValidPromIdent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildHistogramQuantile(t *testing.T) {
	cases := []struct {
		name                       string
		q                          float64
		metric, matchers           string
		window, groupBy            string
		want                       string
	}{
		{
			"no matchers no groupBy",
			0.99, "http_request_duration_seconds", "", "5m", "",
			"histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds[5m])))",
		},
		{
			"matchers no groupBy",
			0.95, "http_request_duration_seconds", `job="api"`, "1m", "",
			`histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds{job="api"}[1m])))`,
		},
		{
			"matchers + groupBy",
			0.5, "latency", `service="foo"`, "10m", "instance",
			`histogram_quantile(0.5, sum by (le, instance) (rate(latency{service="foo"}[10m])))`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildHistogramQuantile(c.q, c.metric, c.matchers, c.window, c.groupBy)
			if got != c.want {
				t.Errorf("buildHistogramQuantile:\n got  = %s\n want = %s", got, c.want)
			}
		})
	}
}

func TestQualifyTempoTag(t *testing.T) {
	cases := []struct {
		scope, tag, want string
	}{
		{"", "", ""},
		{"span", "", ""},
		{"", "service.name", "service.name"},            // intrinsic scope = bare
		{"intrinsic", "duration", "duration"},            // intrinsic scope = bare
		{"span", "service.name", "span.service.name"},
		{"resource", "cluster", "resource.cluster"},
		{"event", "name", "event.name"},
		{"link", "target", "link.target"},
		{"RESOURCE", "case", "RESOURCE.case"},            // case-insensitive match, original case preserved on output
		{"span", "intrinsic:already.scoped", "intrinsic:already.scoped"}, // `:` means already qualified → passthrough
		{"weird", "x", "weird.x"},                        // unknown scope still qualifies (default arm)
	}
	for _, c := range cases {
		if got := qualifyTempoTag(c.scope, c.tag); got != c.want {
			t.Errorf("qualifyTempoTag(%q, %q) = %q, want %q", c.scope, c.tag, got, c.want)
		}
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
