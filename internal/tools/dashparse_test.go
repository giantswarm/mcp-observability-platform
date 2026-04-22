package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestExpandGrafanaVars_LengthDescSort is the regression guard for the
// subtle bug where `$cluster` replaces before `$cluster_id` and corrupts
// the longer name to `<val>_id`. The length-DESC sort in expandGrafanaVars
// prevents this; reordering the sort direction would break here.
func TestExpandGrafanaVars_LengthDescSort(t *testing.T) {
	vars := map[string]string{
		"cluster":    "prod",
		"cluster_id": "42",
	}
	// Query uses both — the longer name must NOT be consumed by the shorter
	// one's replacement.
	got := expandGrafanaVars(`rate(up{cluster="$cluster",id="$cluster_id"}[5m])`, vars, "", "", "1m")
	want := `rate(up{cluster="prod",id="42"}[5m])`
	if got != want {
		t.Errorf("length-DESC sort regression:\n got = %q\nwant = %q", got, want)
	}
}

func TestExpandGrafanaVars_BraceAndBareForms(t *testing.T) {
	vars := map[string]string{"env": "prod"}
	cases := []struct {
		expr, want string
	}{
		{`up{env="$env"}`, `up{env="prod"}`},
		{`up{env="${env}"}`, `up{env="prod"}`},
		{`sum by (env) (up) / on(env) ${env}`, `sum by (env) (up) / on(env) prod`},
	}
	for _, c := range cases {
		if got := expandGrafanaVars(c.expr, vars, "", "", "1m"); got != c.want {
			t.Errorf("expand(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestExpandGrafanaVars_BuiltIns(t *testing.T) {
	// Built-ins resolve to step / computed range. Empty step defaults to 5m.
	cases := []struct {
		expr, start, end, step string
		want                   string
	}{
		{`rate(up[$__rate_interval])`, "", "", "30s", `rate(up[30s])`},
		{`rate(up[$__interval])`, "", "", "", `rate(up[5m])`}, // default step = 5m
		{`$__interval_ms`, "", "", "1m", "60000"},
		{`$__range`, "2024-01-01T00:00:00Z", "2024-01-01T01:00:00Z", "", "1h0m0s"},
		{`$__range_s`, "2024-01-01T00:00:00Z", "2024-01-01T01:00:00Z", "", "3600"},
	}
	for _, c := range cases {
		if got := expandGrafanaVars(c.expr, nil, c.start, c.end, c.step); got != c.want {
			t.Errorf("expand(%q step=%q) = %q, want %q", c.expr, c.step, got, c.want)
		}
	}
}

func TestParseGrafanaTime(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		check   func(time.Time) bool
	}{
		{"", false, func(t time.Time) bool { return t.IsZero() }},
		{"2024-05-20T10:00:00Z", false, func(tt time.Time) bool {
			return tt.Equal(time.Date(2024, 5, 20, 10, 0, 0, 0, time.UTC))
		}},
		{"1700000000", false, func(tt time.Time) bool {
			// Unix seconds
			return tt.Equal(time.Unix(1700000000, 0))
		}},
		{"1700000000000", false, func(tt time.Time) bool {
			// Unix millis (magnitude > 1e12)
			return tt.Equal(time.UnixMilli(1700000000000))
		}},
		{"not-a-time", false, func(tt time.Time) bool { return tt.IsZero() }},
	}
	for _, c := range cases {
		got := parseGrafanaTime(c.in)
		if !c.check(got) {
			t.Errorf("parseGrafanaTime(%q) = %v, fails check", c.in, got)
		}
	}
}

func TestReadJSONPointer_HappyPath(t *testing.T) {
	doc := []byte(`{"panels":[{"id":1,"title":"A"},{"id":2,"title":"B"}],"meta":{"tags":["x","y"]}}`)
	cases := []struct {
		pointer, want string
	}{
		{"", string(doc)},     // empty → whole doc
		{"/", string(doc)},    // root → whole doc
		{"/panels/0/id", "1"}, // array index then key
		{"/panels/1/title", `"B"`},
		{"/meta/tags", `["x","y"]`},
		{"/meta/tags/1", `"y"`},
	}
	for _, c := range cases {
		got, err := readJSONPointer(doc, c.pointer)
		if err != nil {
			t.Errorf("readJSONPointer(%q) err: %v", c.pointer, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("readJSONPointer(%q) = %s, want %s", c.pointer, got, c.want)
		}
	}
}

// TestReadJSONPointer_RFC6901Escapes covers the two escape sequences that
// are easy to get wrong: ~1 = / and ~0 = ~. Order matters — unescape ~1
// first then ~0, otherwise "~01" (meaning "~1" literal) becomes "/".
func TestReadJSONPointer_RFC6901Escapes(t *testing.T) {
	doc := []byte(`{"a/b":"slash","c~d":"tilde","~1":"literal-slash-key"}`)
	cases := []struct {
		pointer, want string
	}{
		{"/a~1b", `"slash"`},            // /a/b literal → escape / as ~1
		{"/c~0d", `"tilde"`},            // /c~d literal → escape ~ as ~0
		{"/~01", `"literal-slash-key"`}, // /~1 key literal (tilde 0 = ~, then 1) ... actually this is tricky
	}
	for _, c := range cases {
		got, err := readJSONPointer(doc, c.pointer)
		if err != nil {
			t.Errorf("readJSONPointer(%q) err: %v", c.pointer, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("readJSONPointer(%q) = %s, want %s", c.pointer, got, c.want)
		}
	}
}

func TestReadJSONPointer_ErrorCases(t *testing.T) {
	doc := []byte(`{"panels":[{"id":1}],"leaf":5}`)
	cases := []struct {
		pointer, wantErrSubstring string
	}{
		{"no-leading-slash", "must start with '/'"},
		{"/missing", "not found"},
		{"/panels/notanum", "expected array index"},
		{"/panels/99", "out of range"},
		{"/leaf/further", "traverses non-container"},
	}
	for _, c := range cases {
		_, err := readJSONPointer(doc, c.pointer)
		if err == nil {
			t.Errorf("readJSONPointer(%q) = nil err, want error containing %q", c.pointer, c.wantErrSubstring)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErrSubstring) {
			t.Errorf("readJSONPointer(%q) err = %v, want substring %q", c.pointer, err, c.wantErrSubstring)
		}
	}
}

func TestDatasourceKindFromRef(t *testing.T) {
	templates := []rawTemplateVar{
		{Name: "ds", Type: "datasource", Query: "loki"},
		{Name: "metrics", Type: "datasource", Query: "prometheus"},
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object form prometheus", `{"type":"prometheus","uid":"mimir-prod"}`, "mimir"},
		{"object form loki", `{"type":"loki","uid":"loki-prod"}`, "loki"},
		{"object form tempo", `{"type":"tempo","uid":"tempo-prod"}`, "tempo"},
		{"object form unknown type, uid hint", `{"type":"other","uid":"mimir-cluster"}`, "mimir"},
		{"bare string uid with mimir", `"mimir-prod"`, "mimir"},
		{"bare string prometheus exact", `"prometheus"`, "mimir"},
		{"template var $ds → loki", `"$ds"`, "loki"},
		{"template var ${metrics} → mimir", `"${metrics}"`, "mimir"},
		{"template var unknown name", `"$doesNotExist"`, ""},
		{"empty raw", ``, ""},
		{"junk raw", `"unrelated"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := datasourceKindFromRef(json.RawMessage(c.raw), templates)
			if got != c.want {
				t.Errorf("datasourceKindFromRef(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}
