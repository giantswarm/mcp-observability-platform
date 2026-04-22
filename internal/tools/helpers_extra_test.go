package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// mkReq builds a CallToolRequest with the given arguments as a JSON map.
// Matches the shape mcp-go uses at the handler boundary: Params.Arguments is
// typed `any`, but the GetString/GetInt/GetFloat accessors decode from a
// map[string]any underneath.
func mkReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = "test_tool"
	req.Params.Arguments = args
	return req
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
		if got := histogramQuantileArg(mkReq(args)); got != c.want {
			t.Errorf("histogramQuantileArg(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPromSelectorArgs_PassesThroughSetFields(t *testing.T) {
	q := promSelectorArgs(mkReq(map[string]any{
		"match": `up{job="api"}`,
		"start": "2024-01-01T00:00:00Z",
		"end":   "2024-01-02T00:00:00Z",
	}))
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
	q := promSelectorArgs(mkReq(nil))
	if len(q) != 0 {
		t.Errorf("empty input should produce empty url.Values, got %v", q)
	}
}

func TestTemplateVarsToMap_SkipsDatasourceVarsAndHandlesAll(t *testing.T) {
	vars := []rawTemplateVar{
		{Name: "ds", Type: "datasource", Query: "prometheus"},
	}
	// Non-datasource vars: a plain one, an $__all var (should become ".+")
	// and an unset one (also becomes ".+"). Current.Value is an `any` — plug
	// in concrete types the code expects.
	cluster := rawTemplateVar{Name: "cluster", Type: "custom"}
	cluster.Current.Value = "prod"
	env := rawTemplateVar{Name: "env", Type: "custom"}
	env.Current.Value = "$__all"
	missing := rawTemplateVar{Name: "missing", Type: "custom"}
	missing.Current.Value = ""
	vars = append(vars, cluster, env, missing)

	got := templateVarsToMap(vars)

	if _, has := got["ds"]; has {
		t.Errorf("datasource-typed var should be skipped: %v", got)
	}
	if got["cluster"] != "prod" {
		t.Errorf("cluster = %q, want prod", got["cluster"])
	}
	if got["env"] != ".+" {
		t.Errorf("$__all should become regex '.+', got %q", got["env"])
	}
	if got["missing"] != ".+" {
		t.Errorf("empty value should default to '.+', got %q", got["missing"])
	}
}

func TestGrafanaTimeArg_ProvidedValueWins(t *testing.T) {
	got := grafanaTimeArg(mkReq(map[string]any{"start": "2024-01-01T00:00:00Z"}), "start", -1*time.Hour)
	if got != "2024-01-01T00:00:00Z" {
		t.Errorf("provided start should pass through, got %q", got)
	}
}

func TestGrafanaTimeArg_MissingValueUsesOffsetFromNow(t *testing.T) {
	// With offset = -1h, the result should be a unix-millis stamp roughly
	// 1 hour in the past. Allow generous slack — the test is only
	// asserting "it's a timestamp near now-1h", not an exact equality.
	before := time.Now().Add(-1 * time.Hour).Add(-5 * time.Second).UnixMilli()
	after := time.Now().Add(-1 * time.Hour).Add(5 * time.Second).UnixMilli()
	got := grafanaTimeArg(mkReq(nil), "start", -1*time.Hour)
	var stamp int64
	_, err := fmtSscanInt64(got, &stamp)
	if err != nil {
		t.Fatalf("grafanaTimeArg output %q is not an integer: %v", got, err)
	}
	if stamp < before || stamp > after {
		t.Errorf("grafanaTimeArg offset = %d, want between %d and %d", stamp, before, after)
	}
}

func TestPickPanelTarget_FindsNestedInRow(t *testing.T) {
	// Dashboard with a row containing panel id 2; pickPanelTarget should
	// descend through rows and find it.
	doc := json.RawMessage(`{
		"dashboard":{
			"panels":[
				{"id":1,"type":"timeseries","datasource":{"type":"prometheus","uid":"mimir"},"targets":[{"refId":"A","expr":"up"}]},
				{"id":10,"type":"row","panels":[
					{"id":2,"type":"timeseries","datasource":{"type":"loki","uid":"loki"},"targets":[{"refId":"A","query":"{job=\"x\"}"}]}
				]}
			],
			"templating":{"list":[]}
		}
	}`)
	panel, target, kind, _, err := pickPanelTarget(doc, 2, 0)
	if err != nil {
		t.Fatalf("pickPanelTarget: %v", err)
	}
	if panel.ID != 2 {
		t.Errorf("panel.ID = %d, want 2", panel.ID)
	}
	if target.Query != `{job="x"}` {
		t.Errorf("target.Query = %q, want {job=\"x\"}", target.Query)
	}
	if kind != "loki" {
		t.Errorf("kind = %q, want loki", kind)
	}
}

func TestPickPanelTarget_NotFound(t *testing.T) {
	doc := json.RawMessage(`{"dashboard":{"panels":[{"id":1}],"templating":{"list":[]}}}`)
	_, _, _, _, err := pickPanelTarget(doc, 999, 0)
	if err == nil {
		t.Fatal("expected error for missing panel id")
	}
}

func TestPickPanelTarget_TargetIdxOutOfRange(t *testing.T) {
	doc := json.RawMessage(`{"dashboard":{"panels":[
		{"id":1,"type":"timeseries","datasource":{"type":"prometheus"},"targets":[{"refId":"A","expr":"up"}]}
	],"templating":{"list":[]}}}`)
	_, _, _, _, err := pickPanelTarget(doc, 1, 5)
	if err == nil {
		t.Fatal("expected error for targetIdx out of range")
	}
}

// fmtSscanInt64 parses a decimal int64 from a string without pulling in
// fmt.Sscanf (which is fine but more machinery than needed here).
func fmtSscanInt64(s string, out *int64) (int, error) {
	var n int64
	var read int
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			if i == 0 {
				return 0, &parseIntErr{s: s}
			}
			break
		}
		n = n*10 + int64(c-'0')
		read++
	}
	*out = n
	return read, nil
}

type parseIntErr struct{ s string }

func (e *parseIntErr) Error() string { return "not a decimal integer: " + e.s }
