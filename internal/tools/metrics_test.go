package tools

import (
	"encoding/json"

	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

const promRulesJSON = `{
  "status":"success",
  "data":{"groups":[
    {"name":"grp1","file":"/etc/g1.yaml","rules":[
      {"type":"alerting","name":"HighErrorRate","query":"rate(err[5m]) > 0.1","state":"firing","health":"ok","labels":{"severity":"critical"},"annotations":{"summary":"bad"}},
      {"type":"alerting","name":"DiskWarning","query":"disk > 0.8","state":"inactive","health":"ok","labels":{"severity":"warning"}},
      {"type":"recording","name":"job:rate","query":"sum by (job) (rate(http[1m]))","state":"","health":"ok"}
    ]},
    {"name":"grp2","file":"/etc/g2.yaml","rules":[
      {"type":"alerting","name":"LatencyHigh","query":"p95 > 2","state":"pending","health":"ok","labels":{"severity":"critical"}}
    ]}
  ]}
}`

func TestFlattenAlertRules_FiltersAndSorts(t *testing.T) {
	got, err := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "all", "")
	if err != nil {
		t.Fatalf("flattenAlertRules: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 alert rules, got %d", len(got))
	}
	// Sorted by type then name (all alerts here, so just alpha-by-name).
	wantOrder := []string{"DiskWarning", "HighErrorRate", "LatencyHigh"}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Errorf("order[%d] = %s, want %s", i, got[i].Name, name)
		}
	}
	// State must be preserved.
	if got[1].State != "firing" {
		t.Errorf("HighErrorRate state = %q, want firing", got[1].State)
	}
}

func TestFlattenAlertRules_FilterByState(t *testing.T) {
	got, err := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "firing", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].Name != "HighErrorRate" {
		t.Errorf("state=firing got %+v", got)
	}
}

func TestFlattenAlertRules_FilterByType(t *testing.T) {
	got, _ := flattenAlertRules(json.RawMessage(promRulesJSON), "record", "all", "")
	if len(got) != 1 || got[0].Type != "record" || got[0].Name != "job:rate" {
		t.Errorf("type=record got %+v", got)
	}
}

func TestFlattenAlertRules_FilterByName(t *testing.T) {
	got, _ := flattenAlertRules(json.RawMessage(promRulesJSON), "alert", "all", "disk")
	if len(got) != 1 || got[0].Name != "DiskWarning" {
		t.Errorf("nameContains=disk got %+v", got)
	}
}

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
		name             string
		q                float64
		metric, matchers string
		window, groupBy  string
		want             string
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

func TestHistogramQuantileArg_ClampsToUnitInterval(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{nil, 0.95}, // default
		{0.99, 0.99},
		{0.5, 0.5},
		{-1.0, 0}, // clamp low
		{1.5, 1},  // clamp high
		{0.0, 0},  // exact boundary low
		{1.0, 1},  // exact boundary high
	}
	for _, c := range cases {
		args := map[string]any{}
		if c.in != nil {
			args["quantile"] = c.in
		}
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
		if got := histogramQuantileArg(req); got != c.want {
			t.Errorf("histogramQuantileArg(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPromSelectorArgs_PassesThroughSetFields(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"match": `up{job="api"}`,
		"start": "2024-01-01T00:00:00Z",
		"end":   "2024-01-02T00:00:00Z",
	}}}
	q := promSelectorArgs(req)
	if got := q.Get("match[]"); got != `up{job="api"}` {
		t.Errorf("match[] = %q, want up{job=\"api\"}", got)
	}
	if got := q.Get("start"); got != "2024-01-01T00:00:00Z" {
		t.Errorf("start = %q", got)
	}
	if got := q.Get("end"); got != "2024-01-02T00:00:00Z" {
		t.Errorf("end = %q", got)
	}
}

func TestPromSelectorArgs_EmptyInputOmitsArgs(t *testing.T) {
	q := promSelectorArgs(mcp.CallToolRequest{})
	if len(q) != 0 {
		t.Errorf("empty input should produce empty url.Values, got %v", q)
	}
}
